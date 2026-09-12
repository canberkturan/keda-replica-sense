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

type fakeWorkloads struct {
	items []workloadstore.SamplingWorkload
}

func (f fakeWorkloads) ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error) {
	return f.items, nil
}

type fakeClaims struct {
	claims  []Claim
	claimed bool
}

func (f *fakeClaims) Claim(_ context.Context, c Claim) (*Run, bool, error) {
	f.claims = append(f.claims, c)
	if len(f.claims) > 1 && !f.claimed {
		return nil, false, nil
	}
	return &Run{ID: "run-1"}, true, nil
}
func (*fakeClaims) MarkFailed(context.Context, string, string) error { return nil }

type fakeJobs struct{ jobs []Job }

func (f *fakeJobs) Create(_ context.Context, job Job) error { f.jobs = append(f.jobs, job); return nil }
