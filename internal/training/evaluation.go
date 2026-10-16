package training

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

const EvaluationVersion = "artifact-comparison-v1"

// EvaluationContract records all input/target semantics; source identity alone
// does not capture horizon, timezone or quantile changes.
type EvaluationContract struct {
	Version         string  `json:"version"`
	PolicyRevision  string  `json:"policy_revision"`
	HorizonSeconds  int64   `json:"horizon_seconds"`
	SamplingSeconds int64   `json:"sampling_seconds"`
	SeasonalSeconds int64   `json:"seasonal_seconds"`
	Quantile        float64 `json:"quantile"`
	Timezone        string  `json:"timezone"`
}

func ContractFor(w workloadstore.SamplingWorkload, period time.Duration) EvaluationContract {
	return EvaluationContract{EvaluationVersion, w.Spec.PolicyRevision, int64(w.Spec.Forecast.DecisionHorizon() / time.Second), int64(w.Spec.Forecast.SamplingInterval / time.Second), int64(period / time.Second), w.Spec.Forecast.Quantile, w.Spec.Forecast.BusinessTimezone}
}

func comparableContract(a, b EvaluationContract) bool {
	a.PolicyRevision, b.PolicyRevision = "", ""
	return a == b
}

type EvaluationPolicy struct {
	Mode                       string        `json:"mode"`
	Window                     time.Duration `json:"window"`
	MinDuration                time.Duration `json:"min_duration"`
	MinWindows                 int           `json:"min_windows"`
	MaxWindows                 int           `json:"max_windows"`
	CoverageTolerance          float64       `json:"coverage_tolerance"`
	MinimumImprovement         float64       `json:"minimum_improvement"`
	MaxRelativeUnderprediction float64       `json:"max_relative_underprediction"`
	Rollback                   bool          `json:"rollback_enabled"`
}

func DefaultEvaluationPolicy() EvaluationPolicy {
	return EvaluationPolicy{Mode: "automatic", Window: 48 * time.Hour, MinDuration: 24 * time.Hour, MinWindows: 72, MaxWindows: 288, CoverageTolerance: .03, MinimumImprovement: .03, MaxRelativeUnderprediction: .5}
}

func (p EvaluationPolicy) Validate() error {
	if (p.Mode != "automatic" && p.Mode != "shadow") || p.MinDuration <= 0 || p.Window < p.MinDuration || p.MinWindows < 2 || p.MaxWindows < p.MinWindows || p.MaxWindows > 4096 || !finite(p.CoverageTolerance) || p.CoverageTolerance < 0 || p.CoverageTolerance >= .5 || !finite(p.MinimumImprovement) || p.MinimumImprovement < 0 || p.MinimumImprovement >= 1 || !finite(p.MaxRelativeUnderprediction) || p.MaxRelativeUnderprediction <= 0 || p.MaxRelativeUnderprediction > 1 {
		return fmt.Errorf("invalid model evaluation policy")
	}
	return nil
}

type EvaluationReport struct {
	Version          string              `json:"version"`
	At               time.Time           `json:"at"`
	Action           string              `json:"action"`
	Reason           string              `json:"reason"`
	Detail           string              `json:"detail,omitempty"`
	ExpectedActiveID string              `json:"expected_active_id"`
	PreviousModelID  string              `json:"previous_model_id,omitempty"`
	Contract         EvaluationContract  `json:"contract"`
	Policy           EvaluationPolicy    `json:"policy"`
	Evidence         forecast.Comparison `json:"evidence"`
	Decision         PromotionDecision   `json:"decision"`
}

type EvaluationModel struct {
	forecast.StoredModel
	Status         string
	TrainedUntil   time.Time
	CreatedAt      time.Time
	Contract       EvaluationContract
	Report         EvaluationReport
	ArtifactSHA256 string
}

type EvaluationStore interface {
	// A per-workload session lock prevents concurrent forecasters evaluating or
	// mutating the same lineage. CompleteEvaluation still fences policy/active ID.
	AcquireEvaluation(context.Context, string) (func(), error)
	EvaluationModels(context.Context, workloadstore.SamplingWorkload) ([]EvaluationModel, error)
	CompleteEvaluation(context.Context, workloadstore.SamplingWorkload, EvaluationModel, EvaluationReport) error
}

type EvaluationSamples interface {
	ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error)
}

type EvaluationService struct {
	Store          EvaluationStore
	Samples        EvaluationSamples
	Policy         EvaluationPolicy
	SeasonalPeriod time.Duration
	OnDecision     func(workloadstore.SamplingWorkload, EvaluationReport)
}

// Evaluate runs on a separate bounded worker, never on the inference path.
func (s EvaluationService) Evaluate(ctx context.Context, w workloadstore.SamplingWorkload, now time.Time) error {
	if err := s.Policy.Validate(); err != nil {
		return err
	}
	if s.Store == nil || s.Samples == nil || s.SeasonalPeriod <= 0 {
		return fmt.Errorf("evaluation requires storage, samples and seasonal period")
	}
	release, err := s.Store.AcquireEvaluation(ctx, w.ID)
	if err != nil {
		return err
	}
	if release == nil {
		return nil
	}
	defer release()
	versions, err := s.Store.EvaluationModels(ctx, w)
	if err != nil {
		return err
	}
	var active, candidate, previous *EvaluationModel
	for i := range versions {
		v := &versions[i]
		switch v.Status {
		case "active":
			active = v
		case "candidate":
			candidate = v
		case "retired":
			previous = v
		}
	}
	if active != nil && candidate != nil && !candidate.CreatedAt.After(active.CreatedAt) {
		candidate = nil
	}
	// A rejected/pending candidate must not starve champion revalidation or
	// rollback. Alternate bounded attempts when both artifacts need monitoring.
	if active != nil && candidate != nil && candidate.Report.At.After(active.Report.At) {
		candidate = nil
	}
	target := candidate
	if target == nil {
		target = active
	}
	if target == nil {
		return nil
	}
	expected := ""
	if active != nil {
		expected = active.ID
	}
	contract := ContractFor(w, s.SeasonalPeriod)
	report := EvaluationReport{Version: EvaluationVersion, At: now.UTC(), Action: "withheld", ExpectedActiveID: expected, Contract: contract, Policy: s.Policy}
	// Activation clears neither observations nor models. Legacy candidates with
	// no full contract cannot be promoted using guessed training boundaries.
	finish := func(reason string) error {
		report.Reason = reason
		if err := s.Store.CompleteEvaluation(ctx, w, *target, report); err != nil {
			return err
		}
		if s.OnDecision != nil {
			s.OnDecision(w, report)
		}
		return nil
	}
	legacyActive := target.Status == "active" && target.Contract.Version == "" && target.Engine == w.Spec.Forecast.ModelEngine
	if target.Contract != contract && !legacyActive {
		return finish("incompatible_contract")
	}
	if legacyActive {
		report.Detail = "legacy active artifact requires fresh serving-contract certification; original training metadata is unchanged"
	}
	if target.TrainedUntil.IsZero() || target.TrainedUntil.After(now) {
		return finish("invalid_training_boundary")
	}
	if candidate != nil && now.Sub(candidate.CreatedAt) > max(72*time.Hour, s.Policy.Window+s.Policy.MinDuration) {
		return finish("candidate_expired")
	}
	models := map[string]forecast.Model{"baseline": forecast.SeasonalBaseline{}}
	closeModels := func() {
		for _, model := range models {
			if closer, ok := model.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	}
	defer closeModels()
	load := func(name string, v *EvaluationModel) error {
		digest := sha256.Sum256(v.Artifact)
		if hex.EncodeToString(digest[:]) != v.ArtifactSHA256 {
			return fmt.Errorf("artifact checksum mismatch")
		}
		model, err := forecast.LoadModel(v.StoredModel)
		if err != nil {
			return err
		}
		if q, ok := model.(forecast.UpperQuantileModel); ok && math.Abs(q.OperationalQuantile()-contract.Quantile) > 1e-9 {
			if closer, ok := model.(interface{ Close() }); ok {
				closer.Close()
			}
			return fmt.Errorf("artifact quantile mismatch")
		}
		models[name] = model
		return nil
	}
	if err := load("candidate", target); err != nil {
		return finish("invalid_artifact")
	}
	after := target.TrainedUntil
	// Legacy active artifacts can be compared conservatively using their stored
	// full training-window end, but never their old validation scores.
	if candidate != nil && active != nil {
		// Policy revisions also contain replica bounds and training flags. Those
		// changes must not strand promotion. Compare compatible target semantics;
		// a different engine/quantile/horizon or corrupt artifact uses baseline.
		if active.Engine == target.Engine && (active.Contract.Version == "" || comparableContract(active.Contract, contract)) {
			if err := load("champion", active); err == nil {
				if active.TrainedUntil.After(after) {
					after = active.TrainedUntil
				}
			} else {
				report.Detail = "active artifact unavailable; deterministic baseline reference"
			}
		} else {
			report.Detail = "active artifact target contract differs; deterministic baseline reference"
		}
	}
	rollbackEligible := candidate == nil && active != nil && s.Policy.Rollback && previous != nil && previous.ID == active.Report.PreviousModelID && previous.Contract == contract
	if rollbackEligible {
		if err := load("previous", previous); err != nil {
			return finish("invalid_rollback_artifact")
		}
		if previous.TrainedUntil.After(after) {
			after = previous.TrainedUntil
		}
		// Rollback decisions need post-activation observations, not the same
		// holdout that already selected the new champion.
		if active.ActivatedAt.After(after) {
			after = active.ActivatedAt
		}
	}
	if recent := now.Add(-s.Policy.Window); recent.After(after) {
		after = recent
	}
	sampling := w.Spec.Forecast.SamplingInterval
	if sampling <= 0 {
		return finish("invalid_contract")
	}
	lookback := max(w.Spec.Forecast.TrainingWindow, 2*s.SeasonalPeriod)
	observed, err := s.Samples.ListSamplesForWindow(ctx, w, after.Add(-lookback), now)
	if err != nil {
		return err
	}
	if len(observed) == 0 || now.Sub(observed[len(observed)-1].ObservedAt) > 3*sampling || observed[len(observed)-1].ObservedAt.After(now) {
		return finish("stale_source")
	}
	location, err := time.LoadLocation(contract.Timezone)
	if err != nil {
		return finish("invalid_contract")
	}
	request := forecast.Request{Horizon: time.Duration(contract.HorizonSeconds) * time.Second, SamplingInterval: sampling, SeasonalPeriod: s.SeasonalPeriod, Quantile: contract.Quantile, BusinessLocation: location}
	report.Evidence, err = forecast.CompareArtifacts(ctx, models, observed, request, forecast.ComparisonOptions{After: after, MinDuration: s.Policy.MinDuration, MinWindows: s.Policy.MinWindows, MaxWindows: s.Policy.MaxWindows, Threshold: w.Spec.Source.Threshold, HistoryWindow: w.Spec.Forecast.TrainingWindow})
	if err != nil {
		report.Detail = err.Error()
		return finish("insufficient_or_invalid_evidence")
	}
	if !FreshEvaluationEvidence(report, now) {
		return finish("evaluation_not_current")
	}
	metrics := report.Evidence.Metrics
	var champion *forecast.ValidationMetrics
	if value, ok := metrics["champion"]; ok {
		champion = &value
	}
	report.Decision = EvaluateArtifactPromotion(s.Policy, contract.Quantile, metrics["candidate"], champion, metrics["baseline"])
	if candidate != nil {
		if report.Decision.Promote {
			report.Action = "promote"
			report.PreviousModelID = expected
			if s.Policy.Mode == "shadow" {
				report.Action = "shadow"
			}
		} else {
			report.Action = "rejected"
		}
	} else {
		report.PreviousModelID = active.Report.PreviousModelID
		if CanRevalidate(s.Policy, contract.Quantile, metrics["candidate"], metrics["baseline"]) {
			report.Action = "revalidated"
			report.Decision = PromotionDecision{Reason: "active_revalidation_passed", Reference: "deterministic_baseline"}
		} else {
			report.Action = "degraded"
			report.Decision = PromotionDecision{Reason: "active_revalidation_failed", Reference: "deterministic_baseline"}
		}
		if rollbackEligible && report.Action == "degraded" {
			current := metrics["candidate"]
			rollbackPolicy := s.Policy
			rollbackPolicy.MinimumImprovement = max(.10, rollbackPolicy.MinimumImprovement)
			report.Decision = EvaluateArtifactPromotion(rollbackPolicy, contract.Quantile, metrics["previous"], &current, metrics["baseline"])
			if report.Decision.Promote {
				report.Action = "rollback"
				report.PreviousModelID = active.ID
				target = previous
				if s.Policy.Mode == "shadow" {
					report.Action = "shadow_rollback"
				}
			}
		}
	}
	return finish(report.Decision.Reason)
}

// A serving-contract certificate is earned from fresh observations, including
// for legacy champions. It does not reconstruct or rewrite their fit metadata.
func CanRevalidate(p EvaluationPolicy, q float64, active, baseline forecast.ValidationMetrics) bool {
	return p.Validate() == nil && finite(q) && q > 0 && q < 1 && safeMetrics(p, q, active) && validMetrics(baseline) && baseline.Windows == active.Windows && active.PinballLossP95 <= baseline.PinballLossP95*1.1+1e-9
}

func FreshEvaluationEvidence(r EvaluationReport, now time.Time) bool {
	if r.Policy.Validate() != nil || r.Version != EvaluationVersion || r.Contract.HorizonSeconds <= 0 || r.Contract.SamplingSeconds <= 0 {
		return false
	}
	maximumAge := time.Duration(r.Contract.HorizonSeconds+3*r.Contract.SamplingSeconds) * time.Second
	return r.Evidence.Fingerprint != "" && !r.Evidence.End.After(r.At) && r.Evidence.End.Sub(r.Evidence.Start) >= r.Policy.MinDuration && r.At.Sub(r.Evidence.End) <= maximumAge && !r.At.Before(now.Add(-10*time.Minute)) && !r.At.After(now.Add(time.Minute))
}

func safeMetrics(p EvaluationPolicy, q float64, m forecast.ValidationMetrics) bool {
	return validMetrics(m) && m.Windows >= p.MinWindows && m.Coverage >= q-p.CoverageTolerance && finite(m.MeanUnderprediction) && m.MeanUnderprediction >= 0 && finite(m.MaxRelativeUnderprediction) && m.MaxRelativeUnderprediction >= 0 && m.MaxRelativeUnderprediction <= p.MaxRelativeUnderprediction && finite(m.PeakMeanUnderprediction) && m.PeakMeanUnderprediction >= 0 && finite(m.ExcessReplicaMinutes) && m.ExcessReplicaMinutes >= 0
}

func EvaluateArtifactPromotion(p EvaluationPolicy, q float64, candidate forecast.ValidationMetrics, champion *forecast.ValidationMetrics, baseline forecast.ValidationMetrics) PromotionDecision {
	if err := p.Validate(); err != nil || !finite(q) || q <= 0 || q >= 1 {
		return PromotionDecision{Reason: "invalid_policy"}
	}
	if !safeMetrics(p, q, candidate) {
		return PromotionDecision{Reason: "candidate_safety_gate_failed"}
	}
	reference, name := baseline, "deterministic_baseline"
	if champion != nil {
		reference, name = *champion, "active_champion"
	}
	if !validMetrics(reference) || reference.Windows != candidate.Windows {
		return PromotionDecision{Reason: "incomparable_reference"}
	}
	if candidate.MeanUnderprediction > reference.MeanUnderprediction+1e-9 || candidate.PeakMeanUnderprediction > reference.PeakMeanUnderprediction+1e-9 || candidate.UnderpredictionRate > reference.UnderpredictionRate+1e-9 {
		return PromotionDecision{Reason: "underprediction_regression", Reference: name}
	}
	// Equal, effectively perfect models need no churn. Keep the champion; a
	// first deterministic baseline may bootstrap with identical safe evidence.
	if champion == nil && candidate.PinballLossP95 <= 1e-9 && reference.PinballLossP95 <= 1e-9 {
		return PromotionDecision{Promote: true, Reason: "safe_baseline_equivalent", Reference: name}
	}
	if candidate.PinballLossP95 >= reference.PinballLossP95*(1-p.MinimumImprovement)-1e-9 {
		return PromotionDecision{Reason: "insufficient_loss_improvement", Reference: name}
	}
	return PromotionDecision{Promote: true, Reason: "paired_evaluation_passed", Reference: name}
}

func EncodeContract(contract EvaluationContract) []byte {
	raw, _ := json.Marshal(map[string]any{"evaluation_contract": contract})
	return raw
}
