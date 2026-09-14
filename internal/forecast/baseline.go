// Package forecast contains demand-forecast implementations.
package forecast

import (
	"fmt"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// SeasonalNaive repeats values from the previous season. It is intentionally a
// mandatory, explainable baseline for later ML models to beat.
type SeasonalNaive struct{ PeriodSteps int }

func (m SeasonalNaive) PredictHorizonMax(history []float64, horizonSteps int) (float64, error) {
	if m.PeriodSteps < 1 {
		return 0, fmt.Errorf("period steps must be positive")
	}
	if horizonSteps < 1 {
		return 0, fmt.Errorf("horizon steps must be positive")
	}
	if len(history) < m.PeriodSteps {
		return 0, fmt.Errorf("history has %d points, need one %d-point season", len(history), m.PeriodSteps)
	}
	var maximum float64
	for step := 0; step < horizonSteps; step++ {
		value := history[len(history)-m.PeriodSteps+step%m.PeriodSteps]
		if step == 0 || value > maximum {
			maximum = value
		}
	}
	return maximum, nil
}

type Request struct {
	Horizon          time.Duration
	SamplingInterval time.Duration
	SeasonalPeriod   time.Duration
	// Quantile is the upper operational quantile used for predictive scaling.
	// A zero value preserves the historical default of 0.95 for direct callers;
	// ScaledObject parsing always supplies a strictly valid configured value.
	Quantile float64
	// BusinessLocation is the workload's declared IANA timezone. Candidate
	// feature models must use it rather than the process-local timezone.
	BusinessLocation *time.Location
}

// PredictSamples adapts canonical samples to a duration-aware baseline.
func (m SeasonalNaive) PredictSamples(samples []domain.Sample, request Request) (float64, error) {
	if request.Horizon <= 0 || request.SamplingInterval <= 0 || request.SeasonalPeriod <= 0 {
		return 0, fmt.Errorf("horizon, sampling interval, and seasonal period must be positive")
	}
	if request.Horizon%request.SamplingInterval != 0 || request.SeasonalPeriod%request.SamplingInterval != 0 {
		return 0, fmt.Errorf("horizon and seasonal period must be whole sampling intervals")
	}
	values := make([]float64, len(samples))
	for i := range samples {
		values[i] = samples[i].ObservedValue
	}
	model := SeasonalNaive{PeriodSteps: int(request.SeasonalPeriod / request.SamplingInterval)}
	return model.PredictHorizonMax(values, int(request.Horizon/request.SamplingInterval))
}
