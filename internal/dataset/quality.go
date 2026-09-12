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

func Validate(samples []domain.Sample, policy QualityPolicy) error {
	if len(samples) < policy.MinSamples {
		return fmt.Errorf("dataset has %d samples, need at least %d", len(samples), policy.MinSamples)
	}
	if policy.SamplingInterval <= 0 {
		return fmt.Errorf("sampling interval must be positive")
	}
	maxGap := time.Duration(policy.MaxGapIntervals) * policy.SamplingInterval
	if maxGap <= 0 {
		maxGap = 2 * policy.SamplingInterval
	}
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
