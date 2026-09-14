package forecast

import (
	"fmt"
	"math"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// ValidationMetrics are walk-forward horizon-maximum metrics. No random split
// is used because future samples must never appear in an earlier forecast.
type ValidationMetrics struct {
	Windows             int     `json:"windows"`
	Coverage            float64 `json:"coverage"`
	UnderpredictionRate float64 `json:"underprediction_rate"`
	MeanAbsoluteError   float64 `json:"mean_absolute_error"`
	// PinballLossP95 retains its persisted JSON name for compatibility and is
	// measured at the request's configured upper operational quantile.
	PinballLossP95 float64 `json:"pinball_loss_p95"`
}

func WalkForward(model Model, samples []domain.Sample, request Request, maxWindows int) (ValidationMetrics, error) {
	if model == nil {
		return ValidationMetrics{}, fmt.Errorf("model is required")
	}
	if request.Horizon <= 0 || request.SamplingInterval <= 0 {
		return ValidationMetrics{}, fmt.Errorf("positive horizon and sampling interval are required")
	}
	horizonSteps := int(request.Horizon / request.SamplingInterval)
	seasonSteps := int(request.SeasonalPeriod / request.SamplingInterval)
	if horizonSteps < 1 || seasonSteps < 1 || len(samples) < seasonSteps+horizonSteps+1 {
		return ValidationMetrics{}, fmt.Errorf("insufficient samples for walk-forward validation")
	}
	start := seasonSteps
	if maxWindows > 0 && len(samples)-horizonSteps-start > maxWindows {
		start = len(samples) - horizonSteps - maxWindows
	}
	var metrics ValidationMetrics
	for cut := start; cut+horizonSteps <= len(samples); cut++ {
		prediction, err := model.PredictSamples(samples[:cut], request)
		if err != nil {
			return ValidationMetrics{}, err
		}
		actual := samples[cut].ObservedValue
		for _, sample := range samples[cut+1 : cut+horizonSteps] {
			if sample.ObservedValue > actual {
				actual = sample.ObservedValue
			}
		}
		metrics.Windows++
		if prediction.P95 >= actual {
			metrics.Coverage++
		} else {
			metrics.UnderpredictionRate++
		}
		metrics.MeanAbsoluteError += math.Abs(prediction.P95 - actual)
		quantile := operationalQuantile(request)
		if actual >= prediction.P95 {
			metrics.PinballLossP95 += quantile * (actual - prediction.P95)
		} else {
			metrics.PinballLossP95 += (1 - quantile) * (prediction.P95 - actual)
		}
	}
	if metrics.Windows == 0 {
		return ValidationMetrics{}, fmt.Errorf("no validation windows")
	}
	count := float64(metrics.Windows)
	metrics.Coverage /= count
	metrics.UnderpredictionRate /= count
	metrics.MeanAbsoluteError /= count
	metrics.PinballLossP95 /= count
	return metrics, nil
}

func operationalQuantile(request Request) float64 {
	if request.Quantile > 0 && request.Quantile < 1 {
		return request.Quantile
	}
	return 0.95
}
