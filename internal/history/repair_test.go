package history

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/sampler"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type repairStore struct {
	points       []domain.Sample
	beforeInsert func()
	batches      int
}

func (s *repairStore) ListSamplesForWindow(_ context.Context, _ workloadstore.SamplingWorkload, start, end time.Time) ([]domain.Sample, error) {
	var out []domain.Sample
	for _, p := range s.points {
		if !p.ObservedAt.Before(start) && !p.ObservedAt.After(end) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *repairStore) InsertMissingSamples(_ context.Context, w workloadstore.SamplingWorkload, points []domain.Sample) (int, error) {
	if s.beforeInsert != nil {
		s.beforeInsert()
		s.beforeInsert = nil
	}
	s.batches++
	inserted := 0
	present := make(map[time.Time]bool, len(s.points)+len(points))
	for _, old := range s.points {
		present[old.ObservedAt.Truncate(w.Spec.Forecast.SamplingInterval)] = true
	}
	for _, p := range points {
		if !present[p.ObservedAt] {
			s.points = append(s.points, p)
			present[p.ObservedAt] = true
			inserted++
		}
	}
	return inserted, nil
}

type rangeFunc func(context.Context, sampler.RangeQuery) ([]sampler.TimedValue, error)

func (f rangeFunc) QueryRange(ctx context.Context, q sampler.RangeQuery) ([]sampler.TimedValue, error) {
	return f(ctx, q)
}
func rangePoints(q sampler.RangeQuery) []sampler.TimedValue {
	var out []sampler.TimedValue
	for at := q.Start; !at.After(q.End); at = at.Add(q.Step) {
		out = append(out, sampler.TimedValue{ObservedAt: at, Value: 123})
	}
	return out
}
func repairFixture(slots int) (Repairer, *repairStore, workloadstore.SamplingWorkload, time.Time, time.Time) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Duration(slots) * time.Minute)
	w := workloadstore.SamplingWorkload{ID: "workload", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, SourceFingerprint: "fp", PolicyRevision: "policy", Source: domain.PrometheusSource{ServerAddress: "http://prometheus:9090", Query: `original_metric{scenario="current"}`}, Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: end.Sub(start)}}}
	store := &repairStore{}
	r := Repairer{Store: store, Policy: DefaultPolicy(), Querier: rangeFunc(func(_ context.Context, q sampler.RangeQuery) ([]sampler.TimedValue, error) {
		return rangePoints(q), nil
	})}
	return r, store, w, start, end
}

func TestRepairFillsLeadingInteriorTrailingGapsAndPreservesRealValues(t *testing.T) {
	r, store, w, start, end := repairFixture(60)
	var queries []sampler.RangeQuery
	for i := 0; i < 60; i++ {
		if i < 2 || i == 20 || i >= 58 {
			continue
		}
		store.points = append(store.points, domain.Sample{ObservedAt: start.Add(time.Duration(i)*time.Minute + 5*time.Second), ObservedValue: 17})
	}
	r.Querier = rangeFunc(func(_ context.Context, q sampler.RangeQuery) ([]sampler.TimedValue, error) {
		queries = append(queries, q)
		if q.Query != w.Spec.Source.Query || q.ServerAddress != w.Spec.Source.ServerAddress || q.Step != time.Minute || !q.End.Before(end) {
			t.Fatal("source contract changed")
		}
		return rangePoints(q), nil
	})
	report, err := r.Repair(context.Background(), w, start, end)
	if err != nil || report.MissingBefore != 5 || report.Inserted != 5 || report.MissingAfter != 0 || len(queries) != 3 {
		t.Fatalf("repair: %+v %v", report, err)
	}
	for _, p := range store.points {
		if p.ObservedAt.Second() == 5 && p.ObservedValue != 17 {
			t.Fatal("existing observations overwritten")
		}
	}
	report, err = r.Repair(context.Background(), w, start, end)
	if err != nil || report.Requests != 0 || report.Inserted != 0 {
		t.Fatal("complete history queried or rewritten", report, err)
	}
}

func TestRepairBootstrapsEmptyHistoryInBoundedChunks(t *testing.T) {
	r, store, w, start, end := repairFixture(25000)
	requests := 0
	r.Querier = rangeFunc(func(_ context.Context, q sampler.RangeQuery) ([]sampler.TimedValue, error) {
		requests++
		if q.End.Sub(q.Start)/q.Step >= 10000 {
			t.Fatal("unbounded range")
		}
		return rangePoints(q), nil
	})
	report, err := r.Repair(context.Background(), w, start, end)
	if err != nil || requests != 3 || store.batches != 3 || report.Inserted != 25000 {
		t.Fatalf("bootstrap: %+v %v", report, err)
	}
}

func TestRepairCoalescesFragmentedHistoryWithoutOverwritingKnownSlots(t *testing.T) {
	r, store, w, start, end := repairFixture(1024)
	for i := 1; i < 1024; i += 2 {
		store.points = append(store.points, domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: 17})
	}
	report, err := r.Repair(context.Background(), w, start, end)
	if err != nil || report.Requests != 1 || report.Inserted != 512 {
		t.Fatalf("fragmented repair: %+v %v", report, err)
	}
	for _, p := range store.points {
		if int(p.ObservedAt.Sub(start)/time.Minute)%2 == 1 && p.ObservedValue != 17 {
			t.Fatal("coalescing changed known observations")
		}
	}
}

func TestRepairRejectsMissingSourceHistoryAndMalformedResponses(t *testing.T) {
	for _, kind := range []string{"empty", "partial", "duplicate", "nonfinite", "negative", "off_grid", "outside", "too_many", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			r, _, w, start, end := repairFixture(10)
			r.Querier = rangeFunc(func(_ context.Context, q sampler.RangeQuery) ([]sampler.TimedValue, error) {
				values := rangePoints(q)
				switch kind {
				case "empty":
					return nil, nil
				case "partial":
					return values[:9], nil
				case "duplicate":
					values[1] = values[0]
				case "nonfinite":
					values[0].Value = math.NaN()
				case "negative":
					values[0].Value = -1
				case "off_grid":
					values[0].ObservedAt = values[0].ObservedAt.Add(time.Second)
				case "outside":
					values[0].ObservedAt = end
				case "too_many":
					return append(values, values[0]), nil
				case "unavailable":
					return nil, errors.New("URL includes original_metric and private labels")
				}
				return values, nil
			})
			report, err := r.Repair(context.Background(), w, start, end)
			if err == nil {
				t.Fatal("unsafe history accepted", kind)
			}
			if strings.Contains(err.Error(), "private labels") {
				t.Fatal("query details leaked")
			}
			if kind == "partial" && report.Inserted != 9 {
				t.Fatal("valid partial repair not retained")
			}
		})
	}
}

func TestRepairHonorsBudgetsCancellationAndConcurrentSampler(t *testing.T) {
	r, store, w, start, end := repairFixture(10)
	r.Policy.MaxPoints = 9
	if report, err := r.Repair(context.Background(), w, start, end); err == nil || report.Requests != 0 {
		t.Fatal("point budget ignored")
	}
	r.Policy = DefaultPolicy()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Repair(ctx, w, start, end); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	store.beforeInsert = func() {
		store.points = append(store.points, domain.Sample{ObservedAt: start.Add(5 * time.Second), ObservedValue: 999})
	}
	report, err := r.Repair(context.Background(), w, start, end)
	if err != nil || report.Inserted != 9 || store.points[0].ObservedValue != 999 {
		t.Fatalf("sampler race: %+v %v", report, err)
	}
}

func TestRepairQueryDeadlineIsBounded(t *testing.T) {
	r, _, w, start, end := repairFixture(10)
	r.Policy.Timeout = 100 * time.Millisecond
	r.Policy.QueryTimeout = 5 * time.Millisecond
	r.Querier = rangeFunc(func(ctx context.Context, _ sampler.RangeQuery) ([]sampler.TimedValue, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	report, err := r.Repair(context.Background(), w, start, end)
	if err == nil || report.Requests != 2 {
		t.Fatalf("retry/deadline bounds: %+v %v", report, err)
	}
}

func TestRepairPolicyRejectsUnsafeEnvironment(t *testing.T) {
	for name, value := range map[string]string{"REPLICASENSE_TRAINER_HISTORY_REPAIR_TIMEOUT": "0s", "REPLICASENSE_TRAINER_HISTORY_QUERY_TIMEOUT": "3h", "REPLICASENSE_TRAINER_HISTORY_MAX_POINTS": "3000000"} {
		if _, err := PolicyFromEnvironment(func(key string) string {
			if key == name {
				return value
			}
			return ""
		}); err == nil {
			t.Fatal("invalid policy accepted", name)
		}
	}
}
