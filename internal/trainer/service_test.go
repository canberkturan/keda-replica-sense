package trainer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/history"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestServicePublishesCandidateWithoutActivation(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, SourceFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Forecast: domain.ForecastConfig{ModelEngine: "seasonal-baseline", TrainingWindow: 24 * time.Hour, SamplingInterval: time.Hour, Horizon: time.Hour}}}
	values := make([]domain.Sample, 49)
	for i := range values {
		values[i] = domain.Sample{ObservedAt: now.Add(-48 * time.Hour).Add(time.Duration(i) * time.Hour), ObservedValue: 10}
	}
	runs, models := &fakeRuns{}, &fakeModels{}
	service := Service{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Samples: fakeSamples{items: values}, Runs: runs, Models: models}
	if err := service.Run(context.Background(), "lab", "w1", "run-1", "seasonal-baseline", now); err != nil {
		t.Fatal(err)
	}
	if !runs.running || runs.modelID != "model-1" || models.candidate.Engine != "seasonal-baseline" || models.activatedID != "" {
		t.Fatalf("runs=%#v model=%#v", runs, models.candidate)
	}
	if !models.candidate.TrainingWindowEnd.Before(values[len(values)-1].ObservedAt) {
		t.Fatal("holdout leaked into fitting boundary")
	}
	var config struct {
		Contract training.EvaluationContract `json:"evaluation_contract"`
	}
	if err := json.Unmarshal(models.candidate.Hyperparameters, &config); err != nil || config.Contract.Version != training.EvaluationVersion {
		t.Fatal("candidate requires evaluation contract")
	}
}

func TestServicePersistsRejectedCandidateAndMarksRunSucceeded(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, SourceFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Forecast: domain.ForecastConfig{ModelEngine: "seasonal-baseline", TrainingWindow: 48 * time.Hour, SamplingInterval: time.Hour, Horizon: time.Hour}}}
	values := make([]domain.Sample, 49)
	for i := range values {
		values[i] = domain.Sample{ObservedAt: now.Add(-48 * time.Hour).Add(time.Duration(i) * time.Hour), ObservedValue: float64(i)}
	}
	runs, models := &fakeRuns{}, &fakeModels{}
	service := Service{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Samples: fakeSamples{items: values}, Runs: runs, Models: models}
	if err := service.Run(context.Background(), "lab", "w1", "run-1", "seasonal-baseline", now); err != nil {
		t.Fatal(err)
	}
	if runs.modelID != "model-1" || models.activatedID != "" {
		t.Fatalf("candidate must persist without activation: runs=%#v models=%#v", runs, models)
	}
	var stored struct {
		Promotion struct {
			Promote bool   `json:"promote"`
			Reason  string `json:"reason"`
		} `json:"promotion"`
	}
	if err := json.Unmarshal(models.candidate.ValidationMetrics, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Promotion.Promote || stored.Promotion.Reason == "" {
		t.Fatalf("missing rejection audit record: %s", models.candidate.ValidationMetrics)
	}
}

func TestServiceRejectsStalePolicyRevisionBeforeModelCreation(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, PolicyRevision: "current", Forecast: domain.ForecastConfig{ModelEngine: "seasonal-baseline", TrainingWindow: 24 * time.Hour, SamplingInterval: time.Hour, Horizon: time.Hour}}}
	runs, models := &fakeRuns{}, &fakeModels{}
	service := Service{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Samples: fakeSamples{}, Runs: runs, Models: models, ExpectedPolicyRevision: "old"}
	if err := service.Run(context.Background(), "lab", "w1", "run-1", "seasonal-baseline", now); err == nil {
		t.Fatal("stale policy revision must be rejected")
	}
	if !runs.running || runs.failed == "" || models.candidate.Engine != "" {
		t.Fatalf("stale job must fail before creating a candidate: runs=%#v models=%#v", runs, models)
	}
}

func TestFeatureSchemaForXGBoostUsesVersionedFeatureContract(t *testing.T) {
	if got, want := featureSchemaFor("xgboost"), "demand-calendar-lag-v3"; got != want {
		t.Fatalf("feature schema = %q, want %q", got, want)
	}
	if got := featureSchemaFor("seasonal-baseline"); got != "v1" {
		t.Fatalf("baseline schema = %q, want v1", got)
	}
}

type historyFunc func(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) (history.Report, error)

func (f historyFunc) Repair(ctx context.Context, w workloadstore.SamplingWorkload, start, end time.Time) (history.Report, error) {
	return f(ctx, w, start, end)
}

func TestTrainerRepairsBeforeLoadingAndDoesNotFitUnrepairedHistory(t *testing.T) {
	for _, fail := range []bool{false, true} {
		now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		samples := &fakeSamples{}
		w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Forecast: domain.ForecastConfig{TrainingWindow: 48 * time.Hour, SamplingInterval: time.Hour, Horizon: time.Hour, ModelEngine: "seasonal-baseline"}}}
		runs, models := &fakeRuns{}, &fakeModels{}
		called := false
		s := Service{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Samples: samples, Runs: runs, Models: models, History: historyFunc(func(_ context.Context, _ workloadstore.SamplingWorkload, start, end time.Time) (history.Report, error) {
			called = true
			if fail {
				return history.Report{}, errors.New("source history unavailable")
			}
			for at := start; at.Before(end); at = at.Add(time.Hour) {
				samples.items = append(samples.items, domain.Sample{ObservedAt: at, ObservedValue: 10})
			}
			return history.Report{Inserted: 48}, nil
		})}
		err := s.Run(context.Background(), "lab", "w1", "run", "seasonal-baseline", now)
		if !called {
			t.Fatal("history preparation skipped")
		}
		if fail {
			if err == nil || models.candidate.Engine != "" || runs.failed == "" {
				t.Fatal("unrepaired history fitted")
			}
		} else {
			if err != nil || runs.modelID == "" {
				t.Fatal("repaired inputs not loaded", err)
			}
		}
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

type fakeModels struct {
	candidate   training.ModelCandidate
	activatedID string
}

func (f *fakeModels) CreateCandidate(_ context.Context, candidate training.ModelCandidate) (string, error) {
	f.candidate = candidate
	return "model-1", nil
}

func (f *fakeModels) Activate(_ context.Context, id string) error {
	f.activatedID = id
	return nil
}
