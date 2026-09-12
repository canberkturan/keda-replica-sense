package trainer

import (
	"context"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestServiceStoresSeasonalBaselineCandidate(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, SourceFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Forecast: domain.ForecastConfig{ModelEngine: "seasonal-baseline", TrainingWindow: 24 * time.Hour, SamplingInterval: time.Hour, Horizon: time.Hour}}}
	values := make([]domain.Sample, 26)
	for i := range values {
		values[i] = domain.Sample{ObservedAt: now.Add(-24 * time.Hour).Add(time.Duration(i) * time.Hour), ObservedValue: float64(i)}
	}
	runs, models := &fakeRuns{}, &fakeModels{}
	service := Service{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Samples: fakeSamples{items: values}, Runs: runs, Models: models}
	if err := service.Run(context.Background(), "lab", "w1", "run-1", "seasonal-baseline", now); err != nil {
		t.Fatal(err)
	}
	if !runs.running || runs.modelID != "model-1" || models.candidate.Engine != "seasonal-baseline" {
		t.Fatalf("runs=%#v model=%#v", runs, models.candidate)
	}
}

func TestFeatureSchemaForXGBoostUsesVersionedFeatureContract(t *testing.T) {
	if got, want := featureSchemaFor("xgboost"), "demand-calendar-lag-v2"; got != want {
		t.Fatalf("feature schema = %q, want %q", got, want)
	}
	if got := featureSchemaFor("seasonal-baseline"); got != "v1" {
		t.Fatalf("baseline schema = %q, want v1", got)
	}
}

type fakeWorkloads struct {
	items []workloadstore.SamplingWorkload
}

func (f fakeWorkloads) ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error) {
	return f.items, nil
}

type fakeSamples struct{ items []domain.Sample }

func (f fakeSamples) ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error) {
	return f.items, nil
}

type fakeRuns struct {
	running bool
	modelID string
	failed  string
}

func (f *fakeRuns) MarkRunning(context.Context, string) error { f.running = true; return nil }
func (f *fakeRuns) MarkSucceeded(_ context.Context, _ string, modelID string, _ int, _ []byte) error {
	f.modelID = modelID
	return nil
}
func (f *fakeRuns) MarkFailed(_ context.Context, _ string, reason string) error {
	f.failed = reason
	return nil
}

type fakeModels struct{ candidate training.ModelCandidate }

func (f *fakeModels) CreateCandidate(_ context.Context, candidate training.ModelCandidate) (string, error) {
	f.candidate = candidate
	return "model-1", nil
}
