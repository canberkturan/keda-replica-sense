// Package dataset loads and validates canonical time-series data for training.
package dataset

import (
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

type QualityPolicy struct {
	MinSamples       int
	SamplingInterval time.Duration
	MaxGapIntervals  int
}

// MostRecentContiguous returns the suffix after the latest gap that exceeds
// the configured tolerance. It deliberately does not repair or interpolate
// observations: callers can safely resume inference only from source data that
// arrived continuously after an outage.
func MostRecentContiguous(samples []domain.Sample, policy QualityPolicy) []domain.Sample {
	if len(samples) < 2 || policy.SamplingInterval <= 0 {
		return samples
	}
	maxGap := maximumGap(policy)
	start := 0
	for i := 1; i < len(samples); i++ {
		if samples[i].ObservedAt.Sub(samples[i-1].ObservedAt) > maxGap {
			start = i
		}
	}
	return samples[start:]
}

// ValidateFresh ensures the source continued to provide observations until the
// inference point. Validate alone can only inspect gaps between stored rows;
// without this check a forecaster could repeatedly publish predictions from a
// frozen final observation.
func ValidateFresh(samples []domain.Sample, now time.Time, policy QualityPolicy) error {
	if len(samples) == 0 {
		return fmt.Errorf("dataset has no samples")
	}
	maxGap := maximumGap(policy)
	latest := samples[len(samples)-1].ObservedAt
	if latest.After(now) {
		return fmt.Errorf("latest sample is future-dated by %s", latest.Sub(now))
	}
	if age := now.Sub(latest); age > maxGap {
		return fmt.Errorf("latest sample is %s old, exceeds allowed %s", age, maxGap)
	}
	return nil
}

func Validate(samples []domain.Sample, policy QualityPolicy) error {
	if len(samples) < policy.MinSamples {
		return fmt.Errorf("dataset has %d samples, need at least %d", len(samples), policy.MinSamples)
	}
	if policy.SamplingInterval <= 0 {
		return fmt.Errorf("sampling interval must be positive")
	}
	maxGap := maximumGap(policy)
	for i := range samples {
		if math.IsNaN(samples[i].ObservedValue) || math.IsInf(samples[i].ObservedValue, 0) {
			return fmt.Errorf("sample value at index %d must be finite", i)
		}
		if i == 0 {
			continue
		}
		gap := samples[i].ObservedAt.Sub(samples[i-1].ObservedAt)
		if gap <= 0 {
			return fmt.Errorf("samples are not strictly ordered at index %d", i)
		}
		if gap > maxGap {
			return fmt.Errorf("sample gap %s at index %d exceeds allowed %s", gap, i, maxGap)
		}
	}
	return nil
}

func maximumGap(policy QualityPolicy) time.Duration {
	maxGap := time.Duration(policy.MaxGapIntervals) * policy.SamplingInterval
	if maxGap <= 0 {
		return 2 * policy.SamplingInterval
	}
	return maxGap
}
