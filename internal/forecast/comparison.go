package forecast

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// Comparison is evidence from identical, non-overlapping, fully observed
// horizons. Trained artifacts must have consumed no observations after After.
// It never interpolates, extrapolates, refits, or reads estimated inputs.
type Comparison struct {
	Start           time.Time                    `json:"start"`
	End             time.Time                    `json:"end"`
	Fingerprint     string                       `json:"fingerprint"`
	EligibleWindows int                          `json:"eligible_windows"`
	Metrics         map[string]ValidationMetrics `json:"metrics"`
}

type ComparisonOptions struct {
	After         time.Time
	MinDuration   time.Duration
	MinWindows    int
	MaxWindows    int
	Threshold     float64
	HistoryWindow time.Duration
}

func CompareArtifacts(ctx context.Context, models map[string]Model, observed []domain.Sample, request Request, options ComparisonOptions) (Comparison, error) {
	result := Comparison{Metrics: make(map[string]ValidationMetrics)}
	interval := request.SamplingInterval
	if interval <= 0 || request.Horizon <= 0 || request.Horizon%interval != 0 || request.SeasonalPeriod <= 0 || request.SeasonalPeriod%interval != 0 || len(models) == 0 || options.MinWindows < 1 || options.MaxWindows < options.MinWindows || options.MinDuration <= 0 || options.Threshold <= 0 || !finiteValue(options.Threshold) {
		return result, fmt.Errorf("invalid comparison contract")
	}
	// Slot deduplication selects real points only. A duplicate cannot fill a
	// missing slot or count as another independent evaluation window.
	samples := make([]domain.Sample, 0, len(observed))
	for i, point := range observed {
		if !finiteValue(point.ObservedValue) || point.ObservedValue < 0 || (i > 0 && !point.ObservedAt.After(observed[i-1].ObservedAt)) {
			return result, fmt.Errorf("invalid comparison observations")
		}
		if len(samples) > 0 && samples[len(samples)-1].ObservedAt.Truncate(interval).Equal(point.ObservedAt.Truncate(interval)) {
			samples[len(samples)-1] = point
		} else {
			samples = append(samples, point)
		}
	}
	steps, season := int(request.Horizon/interval), int(request.SeasonalPeriod/interval)
	history := season
	for _, model := range models {
		if model == nil {
			return result, fmt.Errorf("nil comparison model")
		}
		if fixed, ok := model.(InferenceLookbackModel); ok {
			history = max(history, fixed.InferenceLookback())
		}
		if model.Engine() == "holt-winters" {
			history = max(history, 2*season)
		}
	}
	// Missing slots invalidate the entire input+target window, not just the
	// target maximum. This avoids assigning good scores to incomplete peaks.
	gaps := make([]int, len(samples))
	segmentStart := make([]int, len(samples))
	for i := 1; i < len(samples); i++ {
		gaps[i] = gaps[i-1]
		segmentStart[i] = segmentStart[i-1]
		if samples[i].ObservedAt.Truncate(interval).Sub(samples[i-1].ObservedAt.Truncate(interval)) != interval {
			gaps[i]++
			segmentStart[i] = i
		}
	}
	var indexes []int
	var lastEnd time.Time
	for cut := history; cut+steps <= len(samples); cut++ {
		origin := samples[cut-1].ObservedAt
		if origin.Before(options.After) || (!lastEnd.IsZero() && origin.Before(lastEnd)) || gaps[cut+steps-1] != gaps[cut-history] {
			continue
		}
		indexes = append(indexes, cut)
		lastEnd = samples[cut+steps-1].ObservedAt
	}
	result.EligibleWindows = len(indexes)
	if len(indexes) < options.MinWindows {
		return result, fmt.Errorf("insufficient_complete_windows: %d < %d", len(indexes), options.MinWindows)
	}
	result.Start, result.End = samples[indexes[0]-1].ObservedAt, samples[indexes[len(indexes)-1]+steps-1].ObservedAt
	if result.End.Sub(result.Start) < options.MinDuration || time.Duration(len(indexes))*request.Horizon < options.MinDuration*9/10 {
		return result, fmt.Errorf("insufficient_evaluation_duration")
	}
	if len(indexes) > options.MaxWindows {
		selected := make([]int, options.MaxWindows)
		for i := range selected {
			selected[i] = indexes[i*(len(indexes)-1)/(len(selected)-1)]
		}
		indexes = selected
	}
	// Peaks are defined by observed horizon maxima, independently of any model.
	actuals := make([]float64, len(indexes))
	for i, cut := range indexes {
		for j := cut; j < cut+steps; j++ {
			actuals[i] = math.Max(actuals[i], samples[j].ObservedValue)
		}
	}
	sorted := append([]float64(nil), actuals...)
	sort.Float64s(sorted)
	peakCutoff := sorted[int(float64(len(sorted)-1)*.9)]
	digest := sha256.New()
	for _, sample := range samples {
		fmt.Fprintf(digest, "%d:%g\n", sample.ObservedAt.UnixNano(), sample.ObservedValue)
	}
	result.Fingerprint = hex.EncodeToString(digest.Sum(nil))
	for name, model := range models {
		var metrics ValidationMetrics
		peakCount := 0
		for index, cut := range indexes {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			start := 0
			if options.HistoryWindow > 0 {
				start = max(0, cut-int(options.HistoryWindow/interval))
			}
			if fixed, ok := model.(InferenceLookbackModel); ok {
				start = max(0, cut-fixed.InferenceLookback())
			}
			start = max(start, segmentStart[cut-1])
			prediction, err := model.PredictSamples(samples[start:cut], request)
			if err != nil {
				return result, fmt.Errorf("evaluate %s: %w", name, err)
			}
			if !finiteValue(prediction.P50) || !finiteValue(prediction.P95) || prediction.P50 < 0 || prediction.P95 < prediction.P50 {
				return result, fmt.Errorf("invalid_prediction: %s", name)
			}
			actual, upper := actuals[index], prediction.P95
			shortfall := math.Max(0, actual-upper)
			metrics.Windows++
			if shortfall > 0 {
				metrics.UnderpredictionRate++
			} else {
				metrics.Coverage++
			}
			metrics.MeanAbsoluteError += math.Abs(actual - upper)
			metrics.MeanUnderprediction += shortfall
			metrics.MaxRelativeUnderprediction = math.Max(metrics.MaxRelativeUnderprediction, shortfall/math.Max(1, actual))
			q := operationalQuantile(request)
			if actual >= upper {
				metrics.PinballLossP95 += q * (actual - upper)
			} else {
				metrics.PinballLossP95 += (1 - q) * (upper - actual)
			}
			metrics.ExcessReplicaMinutes += math.Max(0, math.Ceil(upper/options.Threshold)-math.Ceil(actual/options.Threshold)) * request.Horizon.Minutes()
			if actual >= peakCutoff {
				metrics.PeakMeanUnderprediction += shortfall
				peakCount++
			}
		}
		n := float64(metrics.Windows)
		metrics.Coverage /= n
		metrics.UnderpredictionRate /= n
		metrics.MeanAbsoluteError /= n
		metrics.PinballLossP95 /= n
		metrics.MeanUnderprediction /= n
		if peakCount > 0 {
			metrics.PeakMeanUnderprediction /= float64(peakCount)
		}
		if !finiteValue(metrics.PinballLossP95) || !finiteValue(metrics.MeanAbsoluteError) || !finiteValue(metrics.ExcessReplicaMinutes) {
			return result, fmt.Errorf("nonfinite_comparison_score: %s", name)
		}
		result.Metrics[name] = metrics
	}
	return result, nil
}

func finiteValue(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
