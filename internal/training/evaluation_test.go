package training

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type evaluationFake struct {
	versions []EvaluationModel
	samples  []domain.Sample
	reports  []EvaluationReport
	targets  []string
	locked   bool
}

func (f *evaluationFake) AcquireEvaluation(context.Context, string) (func(), error) {
	if f.locked {
		return nil, nil
	}
	return func() {}, nil
}
func (f *evaluationFake) EvaluationModels(context.Context, workloadstore.SamplingWorkload) ([]EvaluationModel, error) {
	for i := range f.versions {
		digest := sha256.Sum256(f.versions[i].Artifact)
		f.versions[i].ArtifactSHA256 = hex.EncodeToString(digest[:])
	}
	return f.versions, nil
}
func (f *evaluationFake) CompleteEvaluation(_ context.Context, _ workloadstore.SamplingWorkload, target EvaluationModel, r EvaluationReport) error {
	f.reports = append(f.reports, r)
	f.targets = append(f.targets, target.ID)
	return nil
}
func (f *evaluationFake) ListSamplesForWindow(_ context.Context, _ workloadstore.SamplingWorkload, start, end time.Time) ([]domain.Sample, error) {
	var items []domain.Sample
	for _, p := range f.samples {
		if !p.ObservedAt.Before(start) && !p.ObservedAt.After(end) {
			items = append(items, p)
		}
	}
	return items, nil
}

func constantGRU(value float64) []byte {
	network := map[string]any{"weight_ih_l0": make([]float64, 30), "weight_hh_l0": make([]float64, 3), "bias_ih_l0": make([]float64, 3), "bias_hh_l0": make([]float64, 3), "head_weight": make([]float64, 1), "head_bias": value}
	raw, _ := json.Marshal(map[string]any{"engine": "gru", "format": "replicasense-pytorch-gru-v1", "feature_schema": forecast.PyTorchGRUFeatureSchema, "upper_quantile": .97, "sequence_length": 60, "input_size": 10, "hidden_size": 1, "value_scale": 1, "p50": network, "p95": network})
	return raw
}

func evaluationFixture() (EvaluationService, workloadstore.SamplingWorkload, *evaluationFake, time.Time) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(8 * time.Hour)
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, PolicyRevision: "policy", SourceFingerprint: "fingerprint", Source: domain.PrometheusSource{Threshold: 5}, Forecast: domain.ForecastConfig{ModelEngine: "gru", Horizon: 10 * time.Minute, SamplingInterval: time.Minute, Quantile: .97, TrainingWindow: 24 * time.Hour, BusinessTimezone: "UTC"}}}
	p := DefaultEvaluationPolicy()
	p.Window = 6 * time.Hour
	p.MinDuration = 4 * time.Hour
	p.MinWindows = 12
	p.MaxWindows = 24
	f := &evaluationFake{}
	for i := 0; i <= 480; i++ {
		f.samples = append(f.samples, domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: 10})
	}
	contract := ContractFor(w, time.Hour)
	f.versions = []EvaluationModel{{StoredModel: forecast.StoredModel{ID: "new", Engine: "gru", Artifact: constantGRU(10)}, Status: "candidate", CreatedAt: now.Add(-time.Hour), TrainedUntil: start.Add(time.Hour), Contract: contract}}
	return EvaluationService{Store: f, Samples: f, Policy: p, SeasonalPeriod: time.Hour}, w, f, now
}

func TestEvaluationServicePromotesExactSafeArtifact(t *testing.T) {
	s, w, f, now := evaluationFixture()
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	if len(f.reports) != 1 || f.reports[0].Action != "promote" || !f.reports[0].Decision.Promote || f.targets[0] != "new" {
		t.Fatalf("decision %+v", f.reports)
	}
}

func TestEvaluationServiceShadowCannotActivate(t *testing.T) {
	s, w, f, now := evaluationFixture()
	s.Policy.Mode = "shadow"
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	if f.reports[0].Action != "shadow" || !f.reports[0].Decision.Promote {
		t.Fatalf("shadow: %+v", f.reports)
	}
}

func TestEvaluationServiceSafetyWithholds(t *testing.T) {
	for _, kind := range []string{"legacy", "policy", "boundary", "stale", "gap", "recent_champion", "bad_artifact", "wrong_quantile", "expired", "locked"} {
		t.Run(kind, func(t *testing.T) {
			s, w, f, now := evaluationFixture()
			switch kind {
			case "legacy":
				f.versions[0].Contract = EvaluationContract{}
			case "policy":
				f.versions[0].Contract.PolicyRevision = "old"
			case "boundary":
				f.versions[0].TrainedUntil = time.Time{}
			case "stale":
				now = now.Add(time.Hour)
			case "gap":
				f.samples = f.samples[len(f.samples)-30:]
			case "recent_champion":
				v := f.versions[0]
				v.ID = "old"
				v.Status = "active"
				v.CreatedAt = now.Add(-2 * time.Hour)
				v.TrainedUntil = now.Add(-time.Hour)
				f.versions = append(f.versions, v)
			case "bad_artifact":
				f.versions[0].Artifact = []byte(`{}`)
			case "wrong_quantile":
				w.Spec.Forecast.Quantile = .95
				f.versions[0].Contract = ContractFor(w, time.Hour)
			case "expired":
				f.versions[0].CreatedAt = now.Add(-73 * time.Hour)
			case "locked":
				f.locked = true
			}
			if err := s.Evaluate(context.Background(), w, now); err != nil {
				t.Fatal(err)
			}
			if kind == "locked" {
				if len(f.reports) != 0 {
					t.Fatal("ignored peer lock")
				}
				return
			}
			if f.reports[0].Action != "withheld" || f.reports[0].Reason == "" {
				t.Fatalf("unsafe activation: %+v", f.reports)
			}
		})
	}
}

func TestEvaluationServiceUsesFreshChampionNotSavedScores(t *testing.T) {
	s, w, f, now := evaluationFixture()
	v := f.versions[0]
	v.ID = "old"
	v.Status = "active"
	v.CreatedAt = now.Add(-2 * time.Hour)
	f.versions = append(f.versions, v)
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	r := f.reports[0]
	if r.Action != "rejected" || r.Reason != "insufficient_loss_improvement" || r.Evidence.Metrics["champion"].PinballLossP95 != 0 {
		t.Fatalf("tie should retain champion: %+v", r)
	}
}

func TestEvaluationServicePolicyOnlyChangesDoNotStrandPromotion(t *testing.T) {
	s, w, f, now := evaluationFixture()
	v := f.versions[0]
	v.ID = "old"
	v.Status = "active"
	v.CreatedAt = now.Add(-2 * time.Hour)
	v.Contract.PolicyRevision = "previous-bounds"
	f.versions = append(f.versions, v)
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.reports[0].Evidence.Metrics["champion"]; !ok {
		t.Fatal("compatible champion not evaluated after bounds-only policy change")
	}
}

func TestEvaluationServiceDifferentTargetUsesBaselineNotOldScores(t *testing.T) {
	s, w, f, now := evaluationFixture()
	v := f.versions[0]
	v.ID = "old"
	v.Status = "active"
	v.CreatedAt = now.Add(-2 * time.Hour)
	v.Contract.HorizonSeconds += 60
	f.versions = append(f.versions, v)
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	if f.reports[0].Action != "promote" || f.reports[0].Decision.Reference != "deterministic_baseline" {
		t.Fatalf("new target contract stranded: %+v", f.reports[0])
	}
}

func TestLegacyChampionRequiresFreshEvidenceForValidityRenewal(t *testing.T) {
	for _, kind := range []string{"safe", "stale", "insufficient", "wrong_quantile", "poor"} {
		t.Run(kind, func(t *testing.T) {
			s, w, f, now := evaluationFixture()
			f.versions[0].Status = "active"
			f.versions[0].Contract = EvaluationContract{}
			switch kind {
			case "stale":
				now = now.Add(time.Hour)
			case "insufficient":
				f.samples = f.samples[len(f.samples)-20:]
			case "wrong_quantile":
				w.Spec.Forecast.Quantile = .95
			case "poor":
				f.versions[0].Artifact = constantGRU(5)
			}
			if err := s.Evaluate(context.Background(), w, now); err != nil {
				t.Fatal(err)
			}
			if kind == "safe" {
				if f.reports[0].Action != "revalidated" {
					t.Fatalf("safe legacy champion stranded: %+v", f.reports)
				}
			} else if f.reports[0].Action == "revalidated" {
				t.Fatal("unsafe legacy renewal", kind)
			}
		})
	}
}

func TestEvaluationServiceRollbackNeedsFreshSafePreviousArtifact(t *testing.T) {
	for _, mode := range []string{"automatic", "shadow", "disabled", "not_previous", "too_recent"} {
		t.Run(mode, func(t *testing.T) {
			s, w, f, now := evaluationFixture()
			s.Policy.Rollback = true
			active := f.versions[0]
			active.ID = "active"
			active.Status = "active"
			active.Artifact = constantGRU(5)
			active.ActivatedAt = now.Add(-5 * time.Hour)
			active.Report.PreviousModelID = "previous"
			previous := active
			previous.ID = "previous"
			previous.Status = "retired"
			previous.Artifact = constantGRU(10)
			previous.CreatedAt = active.CreatedAt.Add(-time.Hour)
			f.versions = []EvaluationModel{active, previous}
			switch mode {
			case "shadow":
				s.Policy.Mode = "shadow"
			case "disabled":
				s.Policy.Rollback = false
			case "not_previous":
				previous.ID = "unrelated"
				f.versions[1] = previous
			case "too_recent":
				f.versions[0].ActivatedAt = now.Add(-time.Hour)
			}
			if err := s.Evaluate(context.Background(), w, now); err != nil {
				t.Fatal(err)
			}
			action := f.reports[0].Action
			switch mode {
			case "automatic":
				if action != "rollback" || f.targets[0] != "previous" {
					t.Fatalf("rollback: %+v", f.reports)
				}
			case "shadow":
				if action != "shadow_rollback" {
					t.Fatalf("shadow rollback: %+v", f.reports)
				}
			case "too_recent":
				if action != "withheld" {
					t.Fatal(action)
				}
			default:
				if action == "rollback" {
					t.Fatal("unsafe rollback")
				}
			}
		})
	}
}

func TestEvaluationPolicyQuantileSeverityAndImprovement(t *testing.T) {
	p := DefaultEvaluationPolicy()
	reference := forecast.ValidationMetrics{Windows: 100, Coverage: .97, UnderpredictionRate: .03, PinballLossP95: 10, MeanUnderprediction: 2, PeakMeanUnderprediction: 3}
	candidate := reference
	candidate.PinballLossP95 = 9
	if !EvaluateArtifactPromotion(p, .97, candidate, &reference, reference).Promote {
		t.Fatal("safe improvement rejected")
	}
	for _, kind := range []string{"quantile", "severity", "peak", "tiny", "nan", "unequal_windows"} {
		c := candidate
		ref := reference
		switch kind {
		case "quantile":
			c.Coverage = .92
		case "severity":
			c.MaxRelativeUnderprediction = .9
		case "peak":
			c.PeakMeanUnderprediction = 4
		case "tiny":
			c.PinballLossP95 = 9.9
		case "nan":
			c.MeanUnderprediction = math.NaN()
		case "unequal_windows":
			ref.Windows = 99
		}
		if EvaluateArtifactPromotion(p, .97, c, &ref, reference).Promote {
			t.Fatal("unsafe promotion", kind)
		}
	}
}

func TestEvaluationConfigRejectsInvalidSafetySettings(t *testing.T) {
	for name, value := range map[string]string{"REPLICASENSE_MODEL_PROMOTION_MODE": "force", "REPLICASENSE_MODEL_EVALUATION_MIN_DURATION": "0s", "REPLICASENSE_MODEL_MIN_IMPROVEMENT": "NaN", "REPLICASENSE_MODEL_EVALUATION_MAX_WINDOWS": "100000", "REPLICASENSE_MODEL_ROLLBACK_ENABLED": "maybe"} {
		if _, err := EvaluationPolicyFromEnvironment(func(key string) string {
			if key == name {
				return value
			}
			return ""
		}); err == nil {
			t.Fatal("invalid setting accepted", name)
		}
	}
}

func TestFreshEvidenceCannotCertifyUsingOldWindowsAndOneFreshTail(t *testing.T) {
	s, w, f, now := evaluationFixture()
	if err := s.Evaluate(context.Background(), w, now); err != nil {
		t.Fatal(err)
	}
	r := f.reports[0]
	if !FreshEvaluationEvidence(r, now) {
		t.Fatal("fresh report rejected")
	}
	r.Evidence.End = now.Add(-time.Hour)
	if FreshEvaluationEvidence(r, now) {
		t.Fatal("stale windows certified as current")
	}
}
