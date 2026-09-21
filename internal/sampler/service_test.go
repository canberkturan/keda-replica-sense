package sampler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type recoveryStore struct {
	workloads []workloadstore.SamplingWorkload
	coverage  workloadstore.SampleCoverage
	upserts   [][]domain.Sample
}

func (s *recoveryStore) ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error) {
	return s.workloads, nil
}

func (s *recoveryStore) UpsertSamples(_ context.Context, samples []domain.Sample) error {
	s.upserts = append(s.upserts, append([]domain.Sample(nil), samples...))
	return nil
}

func (s *recoveryStore) GetSampleCoverage(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) (workloadstore.SampleCoverage, error) {
	return s.coverage, nil
}

type recoveringQuerier struct {
	instantCalls int
	rangeCalls   []RangeQuery
}

func (q *recoveringQuerier) QueryInstant(context.Context, PrometheusQuery) (float64, error) {
	q.instantCalls++
	if q.instantCalls == 1 {
		return 0, errors.New("Prometheus unavailable")
	}
	return 42, nil
}

func (q *recoveringQuerier) QueryRange(_ context.Context, query RangeQuery) ([]TimedValue, error) {
	q.rangeCalls = append(q.rangeCalls, query)
	return []TimedValue{{ObservedAt: query.Start, Value: 40}}, nil
}

func TestServiceRepairsRecentHistoryAfterQueryRecovers(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	workload := workloadstore.SamplingWorkload{ID: "workload", Spec: domain.WorkloadSpec{
		Key:               domain.WorkloadKey{ClusterID: "cluster"},
		SourceFingerprint: "fingerprint",
		Forecast:          domain.ForecastConfig{SamplingInterval: time.Minute},
	}}
	store := &recoveryStore{workloads: []workloadstore.SamplingWorkload{workload}, coverage: workloadstore.SampleCoverage{NewestObservedAt: start.Add(-5 * time.Minute)}}
	querier := &recoveringQuerier{}
	service := Service{Store: store, Querier: querier, ClusterID: "cluster", JitterWindow: time.Second, Backfiller: &Backfiller{Store: store, Querier: querier, RecoveryWindow: time.Hour}}
	// Keep bootstrap out of this test: this specifically verifies recovery after
	// a previously successful sampler has lost and regained its source query.
	service.bootstrapped.Store(workload.ID+":"+workload.Spec.SourceFingerprint, struct{}{})
	if err := service.RunOnce(context.Background(), start); err == nil {
		t.Fatal("first unavailable query must fail")
	}
	if err := service.RunOnce(context.Background(), start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(querier.rangeCalls) != 1 {
		t.Fatalf("range repair calls = %d, want 1", len(querier.rangeCalls))
	}
	if got, want := querier.rangeCalls[0].Start, start.Add(-4*time.Minute); !got.Equal(want) {
		t.Fatalf("repair started at %s, want %s", got, want)
	}
	if len(store.upserts) != 3 || len(store.upserts[1]) != 1 || len(store.upserts[2]) != 1 || store.upserts[2][0].ObservedValue != 42 {
		t.Fatalf("upserts = %#v, want recovered range and current live sample", store.upserts)
	}
}
