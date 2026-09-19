package training

import (
	"context"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestSchedulerClaimsBeforeCreatingJob(t *testing.T) {
	now := time.Date(2026, 9, 2, 23, 59, 0, 0, time.UTC)
	store := &fakeClaims{}
	jobs := &fakeJobs{}
	w := workloadstore.SamplingWorkload{ID: "workload-a", Spec: domain.WorkloadSpec{Forecast: domain.ForecastConfig{TrainingWindow: 7 * 24 * time.Hour, ModelEngine: "seasonal-baseline"}}}
	s := Scheduler{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, Claims: store, Jobs: jobs, ClusterID: "cluster-a", TrainingInterval: time.Hour}
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(store.claims) != 1 || len(jobs.jobs) != 1 || jobs.jobs[0].RunID != "run-1" {
		t.Fatalf("claims=%#v jobs=%#v", store.claims, jobs.jobs)
	}
	store.claimed = false
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(jobs.jobs) != 1 {
		t.Fatal("duplicate claim created another Job")
	}
}

func TestSchedulerClaimsImmediateTrainingOncePerPolicyRevision(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := &fakeClaims{}
	jobs := &fakeJobs{}
	w := workloadstore.SamplingWorkload{ID: "workload-a", Spec: domain.WorkloadSpec{PolicyRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Forecast: domain.ForecastConfig{ImmediateTraining: true, TrainingWindow: 7 * 24 * time.Hour, SamplingInterval: time.Hour, ModelEngine: "xgboost"}}}
	history := &fakeHistory{coverage: sufficientCoverage(now, w.Spec.Forecast.TrainingWindow, w.Spec.Forecast.SamplingInterval)}
	s := Scheduler{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, History: history, Claims: store, Jobs: jobs, ClusterID: "cluster-a", TrainingInterval: 24 * time.Hour}
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(store.immediate) != 1 || len(jobs.jobs) != 1 || jobs.jobs[0].Engine != "xgboost" {
		t.Fatalf("immediate=%#v jobs=%#v", store.immediate, jobs.jobs)
	}
	store.immediateClaimed = false
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(jobs.jobs) != 1 {
		t.Fatal("same immediate policy revision created another Job")
	}
}

func TestSchedulerWaitsForImmediateTrainingHistory(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := &fakeClaims{}
	jobs := &fakeJobs{}
	w := workloadstore.SamplingWorkload{ID: "workload-a", Spec: domain.WorkloadSpec{PolicyRevision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Forecast: domain.ForecastConfig{ImmediateTraining: true, TrainingWindow: 24 * time.Hour, SamplingInterval: time.Hour, ModelEngine: "xgboost"}}}
	history := &fakeHistory{}
	s := Scheduler{Workloads: fakeWorkloads{items: []workloadstore.SamplingWorkload{w}}, History: history, Claims: store, Jobs: jobs, ClusterID: "cluster-a", TrainingInterval: 24 * time.Hour}
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(store.immediate) != 0 || len(jobs.jobs) != 0 {
		t.Fatalf("immediate run was claimed before history was available: claims=%#v jobs=%#v", store.immediate, jobs.jobs)
	}
	history.coverage = sufficientCoverage(now, w.Spec.Forecast.TrainingWindow, w.Spec.Forecast.SamplingInterval)
	if err := s.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(store.immediate) != 1 || len(jobs.jobs) != 1 {
		t.Fatalf("immediate run did not start after history became available: claims=%#v jobs=%#v", store.immediate, jobs.jobs)
	}
}

type fakeWorkloads struct {
	items []workloadstore.SamplingWorkload
}

func (f fakeWorkloads) ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error) {
	return f.items, nil
}

type fakeHistory struct{ coverage workloadstore.SampleCoverage }

func (f *fakeHistory) GetSampleCoverage(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) (workloadstore.SampleCoverage, error) {
	return f.coverage, nil
}

func sufficientCoverage(now time.Time, window, interval time.Duration) workloadstore.SampleCoverage {
	end := now.UTC().Truncate(interval)
	start := end.Add(-window)
	return workloadstore.SampleCoverage{OldestObservedAt: start, NewestObservedAt: end, Count: int64(window / interval)}
}

type fakeClaims struct {
	claims           []Claim
	claimed          bool
	immediate        []Claim
	immediateClaimed bool
}

func (f *fakeClaims) Claim(_ context.Context, c Claim) (*Run, bool, error) {
	f.claims = append(f.claims, c)
	if !c.ExclusiveSince.IsZero() && len(f.immediate) > 0 {
		return nil, false, nil
	}
	if len(f.claims) > 1 && !f.claimed {
		return nil, false, nil
	}
	return &Run{ID: "run-1"}, true, nil
}
func (f *fakeClaims) ClaimImmediate(_ context.Context, c Claim) (*Run, bool, error) {
	f.immediate = append(f.immediate, c)
	if len(f.immediate) > 1 && !f.immediateClaimed {
		return nil, false, nil
	}
	return &Run{ID: "immediate-run-1"}, true, nil
}
func (*fakeClaims) MarkFailed(context.Context, string, string) error { return nil }

type fakeJobs struct{ jobs []Job }

func (f *fakeJobs) Create(_ context.Context, job Job) error { f.jobs = append(f.jobs, job); return nil }
