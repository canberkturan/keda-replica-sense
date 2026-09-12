package forecast

import (
	"fmt"
	"math"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// HoltWinters is an additive triple-exponential-smoothing baseline. It keeps
// level, trend, and repeating seasonal residuals separate, providing a more
// expressive but still fully auditable comparator for ML candidates.
type HoltWinters struct {
	Alpha float64
	Beta  float64
	Gamma float64
}

func (HoltWinters) Engine() string { return "holt-winters" }

func (m HoltWinters) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	if request.Horizon <= 0 || request.SamplingInterval <= 0 || request.SeasonalPeriod <= 0 || request.Horizon%request.SamplingInterval != 0 || request.SeasonalPeriod%request.SamplingInterval != 0 {
		return Prediction{}, fmt.Errorf("horizon and seasonal period must be positive whole sampling intervals")
	}
	seasonSteps := int(request.SeasonalPeriod / request.SamplingInterval)
	if len(samples) < 2*seasonSteps {
		return Prediction{}, fmt.Errorf("history has %d samples, need two %d-point seasons", len(samples), seasonSteps)
	}
	alpha, beta, gamma := m.parameters()
	values := make([]float64, len(samples))
	for i := range samples {
		if math.IsNaN(samples[i].ObservedValue) || math.IsInf(samples[i].ObservedValue, 0) {
			return Prediction{}, fmt.Errorf("non-finite demand sample")
		}
		values[i] = samples[i].ObservedValue
	}
	level := mean(values[:seasonSteps])
	nextLevel := mean(values[seasonSteps : 2*seasonSteps])
	trend := (nextLevel - level) / float64(seasonSteps)
	seasonals := make([]float64, seasonSteps)
	for i := 0; i < seasonSteps; i++ {
		seasonals[i] = values[i] - level
	}
	for i := seasonSteps; i < len(values); i++ {
		index := i % seasonSteps
		previousLevel := level
		level = alpha*(values[i]-seasonals[index]) + (1-alpha)*(level+trend)
		trend = beta*(level-previousLevel) + (1-beta)*trend
		seasonals[index] = gamma*(values[i]-level) + (1-gamma)*seasonals[index]
	}
	steps := int(request.Horizon / request.SamplingInterval)
	maximum := 0.0
	for step := 1; step <= steps; step++ {
		value := level + float64(step)*trend + seasonals[(len(values)-1+step)%seasonSteps]
		if step == 1 || value > maximum {
			maximum = value
		}
	}
	maximum = math.Max(0, maximum)
	return Prediction{P50: maximum, P95: maximum}, nil
}

func (m HoltWinters) parameters() (float64, float64, float64) {
	alpha, beta, gamma := m.Alpha, m.Beta, m.Gamma
	if alpha == 0 {
		alpha = .30
	}
	if beta == 0 {
		beta = .10
	}
	if gamma == 0 {
		gamma = .20
	}
	return alpha, beta, gamma
}

func mean(values []float64) float64 {
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
