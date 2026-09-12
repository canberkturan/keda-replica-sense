package sampler

import (
	"context"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type RangeQuery struct {
	ServerAddress string
	Query         string
	Start         time.Time
	End           time.Time
	Step          time.Duration
}

type TimedValue struct {
	ObservedAt time.Time
	Value      float64
}

type RangeQuerier interface {
	QueryRange(context.Context, RangeQuery) ([]TimedValue, error)
}

type Backfiller struct {
	Store        workloadstore.HistoryRepository
	Querier      RangeQuerier
	QueryTimeout time.Duration
}

// EnsureHistory range-queries the entire rolling training window when local
// history is insufficient. UPSERT makes re-fetching a partial window safe.
func (b Backfiller) EnsureHistory(ctx context.Context, workload workloadstore.SamplingWorkload, now time.Time) (bool, error) {
	end := now.UTC().Truncate(workload.Spec.Forecast.SamplingInterval)
	start := end.Add(-workload.Spec.Forecast.TrainingWindow)
	coverage, err := b.Store.GetSampleCoverage(ctx, workload, start, end)
	if err != nil {
		return false, err
	}
	if hasSufficientHistory(coverage, start, end, workload.Spec.Forecast.SamplingInterval) {
		return false, nil
	}
	samples := make([]domain.Sample, 0)
	// Prometheus enforces a maximum point count per query. Preserve the desired
	// step and fetch consecutive chunks; sample UPSERT makes boundary overlap safe.
	const maxPoints = 10000
	chunkSpan := time.Duration(maxPoints-1) * workload.Spec.Forecast.SamplingInterval
	for chunkStart := start; !chunkStart.After(end); {
		chunkEnd := chunkStart.Add(chunkSpan)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		queryContext := ctx
		cancel := func() {}
		if b.QueryTimeout > 0 {
			queryContext, cancel = context.WithTimeout(ctx, b.QueryTimeout)
		}
		values, err := b.Querier.QueryRange(queryContext, RangeQuery{ServerAddress: workload.Spec.Source.ServerAddress, Query: workload.Spec.Source.Query, Start: chunkStart, End: chunkEnd, Step: workload.Spec.Forecast.SamplingInterval})
		cancel()
		if err != nil {
			return false, err
		}
		for _, value := range values {
			samples = append(samples, BuildSample(workload, value.ObservedAt, value.Value))
		}
		if chunkEnd.Equal(end) {
			break
		}
		chunkStart = chunkEnd
	}
	return true, b.Store.UpsertSamples(ctx, samples)
}

func hasSufficientHistory(coverage workloadstore.SampleCoverage, start, end time.Time, interval time.Duration) bool {
	if coverage.Count == 0 || interval <= 0 || end.Before(start) {
		return false
	}
	// Endpoint coverage alone cannot detect a long gap after a process outage.
	// Require nearly every expected sampling slot as well; a small allowance
	// accounts for Prometheus scrape alignment and query boundaries.
	expected := int64(end.Sub(start) / interval)
	minimumCount := expected - 2
	if minimumCount < 1 {
		minimumCount = 1
	}
	return coverage.Count >= minimumCount && !coverage.OldestObservedAt.After(start.Add(interval)) && !coverage.NewestObservedAt.Before(end.Add(-2*interval))
}
