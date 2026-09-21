// Package forecaster runs inference over canonical PostgreSQL samples.
package forecaster

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/dataset"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/safety"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type SampleReader interface {
	workloadstore.DatasetRepository
}

type Service struct {
	Samples            SampleReader
	Snapshots          forecast.SnapshotRepository
	SeasonalPeriod     time.Duration
	MinimumSamples     int
	CurrentReplicas    func(context.Context, workloadstore.SamplingWorkload) (float64, error)
	ClusterHealthy     func(context.Context) bool
	MaxAbsoluteStep    float64
	MaxPercentIncrease float64
	// DisableSurgeDetection is an explicit operational switch. Production
	// defaults to the independent surge policy.
	DisableSurgeDetection bool
	SurgePolicy           safety.SurgePolicy
	Models                interface {
		ModelFor(workloadstore.SamplingWorkload) (forecast.Model, bool)
	}
	PredictiveAllowed func(context.Context, workloadstore.SamplingWorkload, float64, float64) bool
	OnForecast        func(workloadstore.SamplingWorkload, domain.ForecastSnapshot)
}

func (s Service) Forecast(ctx context.Context, workload workloadstore.SamplingWorkload, now time.Time) (domain.ForecastSnapshot, error) {
	if s.Samples == nil || s.Snapshots == nil {
		return domain.ForecastSnapshot{}, fmt.Errorf("sample reader and snapshot repository are required")
	}
	interval := workload.Spec.Forecast.SamplingInterval
	end := now.UTC().Truncate(interval)
	start := end.Add(-workload.Spec.Forecast.TrainingWindow)
	samples, err := s.Samples.ListSamplesForWindow(ctx, workload, start, end)
	if err != nil {
		return domain.ForecastSnapshot{}, err
	}
	minimum := s.MinimumSamples
	if minimum < 1 {
		minimum = int(s.SeasonalPeriod / interval)
	}
	policy := dataset.QualityPolicy{MinSamples: minimum, SamplingInterval: interval, MaxGapIntervals: 2}
	// A Prometheus outage must not poison a rolling 30-day inference window for
	// 30 days. Use only the newest continuous source-data segment after a gap.
	// We still require the normal minimum history and never synthesize values.
	recovered := dataset.MostRecentContiguous(samples, policy)
	usedRecoverySegment := len(recovered) != len(samples)
	samples = recovered
	if err := dataset.Validate(samples, policy); err != nil {
		return domain.ForecastSnapshot{}, err
	}
	if err := dataset.ValidateFresh(samples, end, policy); err != nil {
		return domain.ForecastSnapshot{}, err
	}
	period := s.SeasonalPeriod
	if period <= 0 {
		period = 24 * time.Hour
	}
	model, active := forecast.Model(nil), false
	if s.Models != nil {
		model, active = s.Models.ModelFor(workload)
	}
	if !active {
		model, err = forecast.ResolveModel(workload.Spec.Forecast.ModelEngine)
		if err != nil {
			return domain.ForecastSnapshot{}, err
		}
	}
	decisionHorizon := workload.Spec.Forecast.DecisionHorizon()
	location, err := time.LoadLocation(workload.Spec.Forecast.BusinessTimezone)
	if err != nil {
		return domain.ForecastSnapshot{}, fmt.Errorf("load workload business timezone: %w", err)
	}
	prediction, err := model.PredictSamples(samples, forecast.Request{Horizon: decisionHorizon, SamplingInterval: interval, SeasonalPeriod: period, Quantile: workload.Spec.Forecast.Quantile, BusinessLocation: location})
	if err != nil {
		return domain.ForecastSnapshot{}, err
	}
	if math.IsNaN(prediction.P50) || math.IsInf(prediction.P50, 0) || math.IsNaN(prediction.P95) || math.IsInf(prediction.P95, 0) || prediction.P50 < 0 || prediction.P95 < 0 {
		return domain.ForecastSnapshot{}, fmt.Errorf("model returned an invalid demand prediction")
	}
	if workload.Spec.Source.Threshold <= 0 {
		return domain.ForecastSnapshot{}, fmt.Errorf("reactive threshold must be positive")
	}
	surge := safety.SurgeResult{}
	if !s.DisableSurgeDetection {
		surge = detectSurge(samples, interval, surgeLeadTime(workload.Spec.Forecast), s.SurgePolicy)
	}
	raw := prediction.P95
	surgeDemand := 0.0
	reason := "baseline_replicas"
	if usedRecoverySegment {
		reason += ";recovered_after_source_gap"
	}
	if surge.Triggered && surge.Demand > raw {
		raw = surge.Demand
		surgeDemand = surge.Demand
		reason = surge.Reason
	}
	desired := math.Ceil(raw / workload.Spec.Source.Threshold)
	current := float64(0)
	if s.CurrentReplicas != nil {
		current, err = s.CurrentReplicas(ctx, workload)
		if err != nil {
			return domain.ForecastSnapshot{}, err
		}
	}
	healthy := true
	if s.ClusterHealthy != nil {
		healthy = s.ClusterHealthy(ctx)
	}
	capacityAllowed := true
	if s.PredictiveAllowed != nil {
		capacityAllowed = s.PredictiveAllowed(ctx, workload, current, desired)
	}
	step := s.MaxAbsoluteStep
	if step <= 0 {
		step = 3
	}
	percent := s.MaxPercentIncrease
	if percent <= 0 {
		percent = 100
	}
	safe, guardReason := safety.ApplyGuardrails(safety.GuardrailInput{Desired: desired, Current: current, MaxReplicas: workload.Spec.Bounds.Max, MaxAbsoluteStep: step, MaxPercentIncrease: percent, ClusterHealthy: healthy, CapacityAllowed: capacityAllowed})
	reason += ";" + guardReason
	// Keep learned uncertainty and anomaly response independent. ForecastP95 is
	// the model's raw statistical bound for model comparison; SurgeDemand and
	// SafeDemand make the operational protection path independently auditable.
	snapshot := domain.ForecastSnapshot{ClusterID: workload.Spec.Key.ClusterID, WorkloadID: workload.ID, SourceFingerprint: workload.Spec.SourceFingerprint, GeneratedAt: now.UTC(), ObservedAt: samples[len(samples)-1].ObservedAt, ModelEngine: model.Engine(), ForecastHorizon: decisionHorizon, ForecastP50: prediction.P50, ForecastP95: prediction.P95, SurgeDemand: surgeDemand, SafeDemand: safe, SafetyReason: reason}
	if err := s.Snapshots.SaveSnapshot(ctx, snapshot); err != nil {
		return domain.ForecastSnapshot{}, err
	}
	if s.OnForecast != nil {
		s.OnForecast(workload, snapshot)
	}
	return snapshot, nil
}

const defaultSurgeLeadTime = 2 * time.Minute

// surgeLeadTime is limited to when a newly requested replica can become
// useful. It is intentionally independent of the ML forecast horizon.
func surgeLeadTime(config domain.ForecastConfig) time.Duration {
	leadTime := config.StartupLatency + config.SafetyBuffer
	if leadTime <= 0 {
		return defaultSurgeLeadTime
	}
	return leadTime
}

func detectSurge(samples []domain.Sample, interval, leadTime time.Duration, policy safety.SurgePolicy) safety.SurgeResult {
	const baselineSamples = 5
	confirmationSamples := policy.ConfirmationSamples
	if confirmationSamples < 1 {
		confirmationSamples = safety.DefaultSurgePolicy().ConfirmationSamples
	}
	if len(samples) < confirmationSamples+baselineSamples || interval <= 0 {
		return safety.SurgeResult{}
	}
	minutes := interval.Minutes()
	if minutes <= 0 {
		return safety.SurgeResult{}
	}
	// Keep the candidate observations out of the historical percentile. They
	// must be evaluated against history rather than watering down the anomaly.
	historyEnd := len(samples) - confirmationSamples
	slopes := make([]float64, 0, historyEnd-1)
	for i := 1; i < historyEnd; i++ {
		slopes = append(slopes, (samples[i].ObservedValue-samples[i-1].ObservedValue)/minutes)
	}
	if len(slopes) == 0 {
		return safety.SurgeResult{}
	}
	sort.Float64s(slopes)
	p95 := slopes[int(math.Ceil(float64(len(slopes))*0.95))-1]
	for candidate := len(samples) - confirmationSamples; candidate < len(samples); candidate++ {
		var baseline float64
		for i := candidate - baselineSamples; i < candidate; i++ {
			baseline += samples[i].ObservedValue
		}
		baseline /= baselineSamples
		input := safety.SurgeInput{
			CurrentValue:          samples[candidate].ObservedValue,
			RecentBaseline:        baseline,
			CurrentSlopePerMinute: (samples[candidate].ObservedValue - samples[candidate-1].ObservedValue) / minutes,
			HistoricalSlopeP95:    p95,
			LeadTimeMinutes:       leadTime.Minutes(),
		}
		if !safety.IsSurgeCandidate(input, policy) {
			return safety.SurgeResult{}
		}
	}
	last := samples[len(samples)-1].ObservedValue
	previous := samples[len(samples)-2].ObservedValue
	var baseline float64
	for i := len(samples) - baselineSamples - 1; i < len(samples)-1; i++ {
		baseline += samples[i].ObservedValue
	}
	baseline /= baselineSamples
	return safety.DetectSurge(safety.SurgeInput{CurrentValue: last, RecentBaseline: baseline, CurrentSlopePerMinute: (last - previous) / minutes, HistoricalSlopeP95: p95, LeadTimeMinutes: leadTime.Minutes()}, policy)
}
