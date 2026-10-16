// Package trainer executes a single claimed training run inside a Kubernetes Job.
package trainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/dataset"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/features"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/history"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type Workloads interface {
	ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error)
}
type Samples interface {
	ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error)
}
type Runs interface {
	MarkRunning(context.Context, string) error
	MarkSucceeded(context.Context, string, string, int, []byte) error
	MarkFailed(context.Context, string, string) error
}
type Models interface {
	CreateCandidate(context.Context, training.ModelCandidate) (string, error)
}

type HistoryPreparer interface {
	Repair(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) (history.Report, error)
}

type Service struct {
	Workloads              Workloads
	Samples                Samples
	Runs                   Runs
	Models                 Models
	ExpectedPolicyRevision string
	// SeasonalPeriod is normally 24h. It is injected by the scheduler into
	// each Job so an explicitly accelerated laboratory can preserve its
	// simulated calendar without weakening the production default.
	SeasonalPeriod time.Duration
	// MaxGapIntervals bounds tolerated source gaps. The production default is
	// conservative; accelerated labs may opt into a larger value.
	MaxGapIntervals int
	// Holdout observations are never passed to fitting or normalization. The
	// shared Go worker evaluates the exact stored artifact after publication.
	Holdout time.Duration
	History HistoryPreparer
}

func (s Service) Run(ctx context.Context, clusterID, workloadID, runID, engine string, now time.Time) (err error) {
	if s.Workloads == nil || s.Samples == nil || s.Runs == nil || s.Models == nil {
		return fmt.Errorf("trainer requires workloads, samples, runs, and models")
	}
	if err := s.Runs.MarkRunning(ctx, runID); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = s.Runs.MarkFailed(ctx, runID, err.Error())
		}
	}()
	workloads, err := s.Workloads.ListActiveForCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	var workload *workloadstore.SamplingWorkload
	for index := range workloads {
		if workloads[index].ID == workloadID {
			workload = &workloads[index]
			break
		}
	}
	if workload == nil {
		return fmt.Errorf("active workload %s not found", workloadID)
	}
	if s.ExpectedPolicyRevision != "" && workload.Spec.PolicyRevision != s.ExpectedPolicyRevision {
		return fmt.Errorf("training Job policy revision no longer matches workload %s", workloadID)
	}
	if engine == "" {
		engine = workload.Spec.Forecast.ModelEngine
	}
	end := now.UTC().Truncate(workload.Spec.Forecast.SamplingInterval)
	start := end.Add(-workload.Spec.Forecast.TrainingWindow)
	var historyReport history.Report
	if s.History != nil {
		historyReport, err = s.History.Repair(ctx, *workload, start, end)
		if err != nil {
			return fmt.Errorf("prepare training history: %w", err)
		}
	}
	samples, err := s.Samples.ListSamplesForWindow(ctx, *workload, start, end.Add(-time.Nanosecond))
	if err != nil {
		return err
	}
	seasonalPeriod := s.SeasonalPeriod
	if seasonalPeriod <= 0 {
		seasonalPeriod = 24 * time.Hour
	}
	if seasonalPeriod%workload.Spec.Forecast.SamplingInterval != 0 {
		return fmt.Errorf("seasonal period %s must be an exact multiple of sampling interval %s", seasonalPeriod, workload.Spec.Forecast.SamplingInterval)
	}
	minimum := int(seasonalPeriod / workload.Spec.Forecast.SamplingInterval)
	// Never pretend a source outage is a regular model sampling step. An older
	// complete segment is usable, but its actual fitting cutoff stays explicit.
	samples, err = dataset.TrainingSegment(samples, workload.Spec.Forecast.SamplingInterval, minimum+minimum/4)
	if err != nil {
		return err
	}
	maxGapIntervals := s.MaxGapIntervals
	if maxGapIntervals <= 0 {
		maxGapIntervals = 2
	}
	if err := dataset.Validate(samples, dataset.QualityPolicy{MinSamples: minimum, SamplingInterval: workload.Spec.Forecast.SamplingInterval, MaxGapIntervals: maxGapIntervals}); err != nil {
		return err
	}
	location, err := time.LoadLocation(workload.Spec.Forecast.BusinessTimezone)
	if err != nil {
		return fmt.Errorf("load workload business timezone: %w", err)
	}
	request := forecast.Request{Horizon: workload.Spec.Forecast.DecisionHorizon(), SamplingInterval: workload.Spec.Forecast.SamplingInterval, SeasonalPeriod: seasonalPeriod, Quantile: workload.Spec.Forecast.Quantile, BusinessLocation: location}
	var model forecast.Model
	var artifact []byte
	holdout := s.Holdout
	if holdout <= 0 {
		holdout = 48 * time.Hour
	}
	reserved := min(int(holdout/request.SamplingInterval), len(samples)/5)
	if reserved < 1 || len(samples)-reserved < minimum {
		return fmt.Errorf("insufficient training history after reserving holdout")
	}
	fitSamples := samples[:len(samples)-reserved]
	if engine == "xgboost" {
		model, artifact, err = forecast.TrainXGBoost(fitSamples, request)
	} else if engine == "gru" {
		model, artifact, err = forecast.TrainGRU(fitSamples, request)
	} else {
		model, err = forecast.ResolveModel(engine)
		if err == nil {
			artifactMetadata := map[string]any{"format": "replicasense-model-v1", "engine": model.Engine()}
			if model.Engine() == "seasonal-baseline" {
				artifactMetadata["seasonalPeriod"] = "24h"
			}
			artifact, err = json.Marshal(artifactMetadata)
		}
	}
	if err != nil {
		return err
	}
	if closer, ok := model.(interface{ Close() }); ok {
		defer closer.Close()
	}
	contract := training.ContractFor(*workload, seasonalPeriod)
	metrics, err := json.Marshal(map[string]any{"sample_count": len(fitSamples), "holdout_sample_count": reserved, "history_repair": historyReport, "operational_quantile": forecastOperationalQuantile(request), "promotion": training.PromotionDecision{Reason: "awaiting_shared_artifact_evaluation"}})
	if err != nil {
		return err
	}
	datasetDigest := fingerprint(fitSamples)
	artifactHash := sha256.Sum256(artifact)
	featureSchema := featureSchemaFor(model.Engine())
	modelID, err := s.Models.CreateCandidate(ctx, training.ModelCandidate{ClusterID: clusterID, WorkloadID: workload.ID, SourceFingerprint: workload.Spec.SourceFingerprint, Engine: model.Engine(), FeatureSchema: featureSchema, TrainingWindowStart: fitSamples[0].ObservedAt, TrainingWindowEnd: fitSamples[len(fitSamples)-1].ObservedAt, DatasetFingerprint: datasetDigest, Hyperparameters: training.EncodeContract(contract), ValidationMetrics: metrics, Artifact: artifact, ArtifactSHA256: hex.EncodeToString(artifactHash[:])})
	if err != nil {
		return err
	}
	return s.Runs.MarkSucceeded(ctx, runID, modelID, len(fitSamples), metrics)
}

func featureSchemaFor(engine string) string {
	if engine == "xgboost" {
		return features.SchemaV3
	}
	if engine == "gru" {
		return forecast.GRUFeatureSchema
	}
	return "v1"
}

func forecastOperationalQuantile(request forecast.Request) float64 {
	if request.Quantile > 0 && request.Quantile < 1 {
		return request.Quantile
	}
	return 0.95
}

func fingerprint(samples []domain.Sample) string {
	hash := sha256.New()
	for _, sample := range samples {
		_, _ = hash.Write([]byte(sample.ObservedAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatFloat(sample.ObservedValue, 'g', -1, 64) + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
