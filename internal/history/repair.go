// Package history repairs training inputs from the workload's authoritative
// Prometheus query. It never interpolates or replaces existing observations.
package history

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/sampler"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type Policy struct {
	Timeout      time.Duration
	QueryTimeout time.Duration
	MaxPoints    int
}

func DefaultPolicy() Policy {
	return Policy{Timeout: 2 * time.Minute, QueryTimeout: 15 * time.Second, MaxPoints: 1_000_000}
}
func (p Policy) Validate() error {
	if p.Timeout <= 0 || p.Timeout > 30*time.Minute || p.QueryTimeout <= 0 || p.QueryTimeout > p.Timeout || p.QueryTimeout > 2*time.Minute || p.MaxPoints < 1 || p.MaxPoints > 2_000_000 {
		return fmt.Errorf("invalid trainer history repair limits")
	}
	return nil
}
func PolicyFromEnvironment(lookup func(string) string) (Policy, error) {
	p := DefaultPolicy()
	for name, dst := range map[string]*time.Duration{"REPLICASENSE_TRAINER_HISTORY_REPAIR_TIMEOUT": &p.Timeout, "REPLICASENSE_TRAINER_HISTORY_QUERY_TIMEOUT": &p.QueryTimeout} {
		if raw := lookup(name); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return p, fmt.Errorf("%s must be a duration", name)
			}
			*dst = d
		}
	}
	if raw := lookup("REPLICASENSE_TRAINER_HISTORY_MAX_POINTS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return p, fmt.Errorf("invalid trainer history point limit")
		}
		p.MaxPoints = value
	}
	return p, p.Validate()
}

type Store interface {
	ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error)
	InsertMissingSamples(context.Context, workloadstore.SamplingWorkload, []domain.Sample) (int, error)
}
type Report struct {
	Start         time.Time `json:"start"`
	End           time.Time `json:"end_exclusive"`
	Expected      int       `json:"expected_slots"`
	MissingBefore int       `json:"missing_before"`
	MissingAfter  int       `json:"missing_after"`
	Inserted      int       `json:"inserted_samples"`
	Requests      int       `json:"requests"`
}
type Repairer struct {
	Store   Store
	Querier sampler.RangeQuerier
	Policy  Policy
}

// Repair checks the entire configured training window, including leading,
// interior and trailing gaps. End is exclusive: only closed sampling slots are
// required. A partial successful repair is retained for safe idempotent retries.
func (r Repairer) Repair(ctx context.Context, w workloadstore.SamplingWorkload, start, end time.Time) (report Report, err error) {
	report.Start, report.End = start.UTC(), end.UTC()
	if err := r.Policy.Validate(); err != nil {
		return report, err
	}
	interval := w.Spec.Forecast.SamplingInterval
	if r.Store == nil || r.Querier == nil || interval < time.Second || interval%time.Second != 0 || !start.Before(end) || end.Sub(start) > 90*24*time.Hour || end.Sub(start) != w.Spec.Forecast.TrainingWindow || end.Sub(start)%interval != 0 || !start.Equal(start.Truncate(interval)) || !end.Equal(end.Truncate(interval)) || w.Spec.Source.Query == "" || w.Spec.Source.ServerAddress == "" {
		return report, fmt.Errorf("invalid training history repair contract")
	}
	slots := int(end.Sub(start) / interval)
	report.Expected = slots
	if slots > r.Policy.MaxPoints {
		return report, fmt.Errorf("training history exceeds point budget: %d > %d", slots, r.Policy.MaxPoints)
	}
	ctx, cancel := context.WithTimeout(ctx, r.Policy.Timeout)
	defer cancel()
	missing := make([]bool, slots)
	for i := range missing {
		missing[i] = true
	}
	read := func() (int, error) {
		for i := range missing {
			missing[i] = true
		}
		values, err := r.Store.ListSamplesForWindow(ctx, w, start, end.Add(-time.Nanosecond))
		if err != nil {
			return 0, err
		}
		for _, p := range values {
			if p.ObservedAt.Before(start) || !p.ObservedAt.Before(end) || math.IsNaN(p.ObservedValue) || math.IsInf(p.ObservedValue, 0) || p.ObservedValue < 0 {
				return 0, fmt.Errorf("invalid stored training observation")
			}
			missing[int(p.ObservedAt.Sub(start)/interval)] = false
		}
		count := 0
		for _, absent := range missing {
			if absent {
				count++
			}
		}
		return count, nil
	}
	report.MissingBefore, err = read()
	if err != nil {
		return report, err
	}
	report.MissingAfter = report.MissingBefore
	if report.MissingBefore == 0 {
		return report, nil
	}
	// Normally query only contiguous missing runs. Highly fragmented histories
	// are coalesced into bounded shards to avoid thousands of tiny HTTP requests.
	type span struct{ first, last int }
	var plan []span
	for i := 0; i < slots; {
		if !missing[i] {
			i++
			continue
		}
		first := i
		for i < slots && missing[i] && i-first < 10000 {
			i++
		}
		plan = append(plan, span{first, i - 1})
	}
	if len(plan) > 128 {
		plan = nil
		for first := 0; first < slots; first += 10000 {
			last := min(slots, first+10000) - 1
			a, b := first, last
			for a <= b && !missing[a] {
				a++
			}
			for b >= a && !missing[b] {
				b--
			}
			if a <= b {
				plan = append(plan, span{a, b})
			}
		}
	}
	for _, part := range plan {
		query := sampler.RangeQuery{ServerAddress: w.Spec.Source.ServerAddress, Query: w.Spec.Source.Query, Start: start.Add(time.Duration(part.first) * interval), End: start.Add(time.Duration(part.last) * interval), Step: interval}
		var values []sampler.TimedValue
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if report.Requests >= 256 {
				return report, fmt.Errorf("training history request budget exceeded")
			}
			queryCtx, cancel := context.WithTimeout(ctx, r.Policy.QueryTimeout)
			report.Requests++
			values, err = r.Querier.QueryRange(queryCtx, query)
			cancel()
			if err == nil {
				break
			}
		}
		if err != nil {
			return report, fmt.Errorf("Prometheus training history request failed; check source availability and access")
		}
		if len(values) > part.last-part.first+1 {
			return report, fmt.Errorf("Prometheus history returned excess points")
		}
		var samples []domain.Sample
		seen := make(map[int]bool, len(values))
		for _, value := range values {
			// Tolerate sub-millisecond JSON floating-point timestamp encoding,
			// not source jitter, arbitrary timestamps or synthetic observations.
			at := value.ObservedAt.Round(interval)
			delta := value.ObservedAt.Sub(at)
			if delta < -time.Millisecond || delta > time.Millisecond || at.Before(query.Start) || at.After(query.End) || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) || value.Value < 0 {
				return report, fmt.Errorf("invalid Prometheus training history point")
			}
			slot := int(at.Sub(start) / interval)
			if seen[slot] {
				return report, fmt.Errorf("duplicate Prometheus training history slot")
			}
			seen[slot] = true
			if missing[slot] {
				samples = append(samples, sampler.BuildSample(w, at, value.Value))
			}
		}
		if len(samples) > 0 {
			inserted, err := r.Store.InsertMissingSamples(ctx, w, samples)
			if err != nil {
				return report, err
			}
			report.Inserted += inserted
		}
	}
	report.MissingAfter, err = read()
	if err != nil {
		return report, err
	}
	if report.MissingAfter > 0 {
		return report, fmt.Errorf("training history incomplete: %d of %d slots unavailable from Prometheus; check retention/query or reduce trainingWindow", report.MissingAfter, slots)
	}
	return report, nil
}
