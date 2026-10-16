package dataset

import (
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// InferenceHistory is a disposable model input, not an observed data set.
// Estimated marks every interpolated point; callers must never persist Samples
// through the sampler or use them to train/evaluate models or detect surges.
type InferenceHistory struct {
	Samples              []domain.Sample
	Estimated            []bool
	EstimatedCount       int
	UsedContiguousSuffix bool
}

type InterpolationPolicy struct {
	SamplingInterval time.Duration
	MaxGap           time.Duration
	// Lookback is the fixed input length of an already trained model. Zero
	// retains the full history for statistical models that use all observations.
	Lookback int
}

// PrepareInferenceHistory bridges only small, aligned, bounded gaps. A long or
// unreliable gap starts a new real-data segment; normal warm-up requirements
// still apply to that segment. There is no extrapolation, including at the tail.
func PrepareInferenceHistory(observed []domain.Sample, policy InterpolationPolicy) (InferenceHistory, error) {
	var result InferenceHistory
	interval := policy.SamplingInterval
	if interval <= 0 {
		return result, fmt.Errorf("inference sampling interval must be positive")
	}
	maxGap := policy.MaxGap
	if maxGap <= 0 {
		maxGap = 5 * time.Minute
	}
	if policy.Lookback > 0 && len(observed) > 0 {
		// Fixed-window models do not consume month-old values. Keep extra real
		// anchors for a bounded bridge without poisoning recent inputs with
		// unrelated old missing/invalid observations.
		slots := int64(policy.Lookback) + 6
		if slots <= 0 || slots > int64((1<<63-1)/interval) {
			return result, fmt.Errorf("inference lookback is too large")
		}
		cutoff := observed[len(observed)-1].ObservedAt.Add(-time.Duration(slots) * interval)
		start := 0
		for start < len(observed)-1 && observed[start].ObservedAt.Before(cutoff) {
			start++
		}
		observed = observed[start:]
	}
	// Backfill and live sampling can supply multiple real points in the same
	// interval. Keep the latest real point per slot without changing its value
	// or timestamp; duplicate rows must not fake two post-gap confirmations.
	selected := make([]domain.Sample, 0, len(observed))
	for index, sample := range observed {
		if index > 0 && !sample.ObservedAt.After(observed[index-1].ObservedAt) {
			return result, fmt.Errorf("inference observations are not strictly ordered")
		}
		if len(selected) > 0 && selected[len(selected)-1].ObservedAt.Truncate(interval).Equal(sample.ObservedAt.Truncate(interval)) {
			selected[len(selected)-1] = sample
		} else {
			selected = append(selected, sample)
		}
	}
	observed = selected
	for index, sample := range observed {
		if math.IsNaN(sample.ObservedValue) || math.IsInf(sample.ObservedValue, 0) || sample.ObservedValue < 0 {
			return result, fmt.Errorf("inference observation %d is not finite non-negative demand", index)
		}
		if index > 0 {
			previous := observed[index-1]
			gap := sample.ObservedAt.Sub(previous.ObservedAt)
			if gap <= 0 {
				return result, fmt.Errorf("inference observations are not strictly ordered")
			}
			if gap >= interval+interval/2 {
				steps := math.Round(float64(gap) / float64(interval))
				// Bound arithmetic and allocation before converting to an int.
				aligned := math.Abs(float64(gap)-steps*float64(interval)) <= float64(interval)/4
				scale := math.Max(1, math.Max(previous.ObservedValue, sample.ObservedValue))
				stable := math.Abs(sample.ObservedValue-previous.ObservedValue)/scale <= .25
				if gap > maxGap+interval/4 || !aligned || !stable || steps > 6 {
					result.Samples, result.Estimated = nil, nil
					result.UsedContiguousSuffix = true
				} else {
					for step := 1; step < int(steps); step++ {
						point := previous
						point.ObservedAt = previous.ObservedAt.Add(time.Duration(step) * interval)
						weight := float64(point.ObservedAt.Sub(previous.ObservedAt)) / float64(gap)
						// Convex interpolation stays between the actual anchors and
						// avoids overflow from subtracting extreme finite values.
						point.ObservedValue = (1-weight)*previous.ObservedValue + weight*sample.ObservedValue
						result.Samples = append(result.Samples, point)
						result.Estimated = append(result.Estimated, true)
					}
				}
			}
		}
		result.Samples = append(result.Samples, sample)
		result.Estimated = append(result.Estimated, false)
	}
	if policy.Lookback > 0 && len(result.Samples) > policy.Lookback {
		start := len(result.Samples) - policy.Lookback
		result.Samples, result.Estimated = result.Samples[start:], result.Estimated[start:]
	}
	for _, estimated := range result.Estimated {
		if estimated {
			result.EstimatedCount++
		}
	}
	if result.EstimatedCount > 0 {
		if float64(result.EstimatedCount)/float64(len(result.Samples)) > .05 {
			return result, fmt.Errorf("more than 5%% of inference inputs would be estimated")
		}
		// Guard the freshest hour as well when a statistical model consumes a
		// long window; old real history cannot hide a dense recent interruption.
		start := len(result.Estimated) - 60
		if start < 0 {
			start = 0
		}
		recentEstimated := 0
		for _, estimated := range result.Estimated[start:] {
			if estimated {
				recentEstimated++
			}
		}
		if float64(recentEstimated)/float64(len(result.Estimated)-start) > .05 {
			return result, fmt.Errorf("more than 5%% of recent inference inputs would be estimated")
		}
		realTail := 0
		for index := len(result.Estimated) - 1; index >= 0 && !result.Estimated[index]; index-- {
			realTail++
		}
		if realTail < 2 {
			return result, fmt.Errorf("short-gap recovery needs two real post-gap observations")
		}
	}
	return result, nil
}

// RealContiguousSuffix excludes missing sampling slots from surge evidence,
// while allowing normal sampler/backfill timestamp jitter.
func RealContiguousSuffix(observed []domain.Sample, interval time.Duration) []domain.Sample {
	if interval <= 0 {
		return nil
	}
	real := make([]domain.Sample, 0, len(observed))
	for _, sample := range observed {
		if len(real) > 0 {
			previous := real[len(real)-1]
			if previous.ObservedAt.Truncate(interval).Equal(sample.ObservedAt.Truncate(interval)) {
				real[len(real)-1] = sample
				continue
			}
			if sample.ObservedAt.Sub(previous.ObservedAt) >= interval+interval/2 {
				real = real[:0]
			}
		}
		real = append(real, sample)
	}
	return real
}
