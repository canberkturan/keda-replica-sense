package forecast

import (
	"fmt"
	"math"
	"sort"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// RollingQuantile estimates the next horizon peak from empirical historical
// horizon maxima. It is a deterministic quantile model, useful as a second
// transparent baseline alongside optional native model integrations.
type RollingQuantile struct{}

func (RollingQuantile) Engine() string { return "rolling-quantile" }

func (RollingQuantile) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	if request.Horizon <= 0 || request.SamplingInterval <= 0 || request.Horizon%request.SamplingInterval != 0 {
		return Prediction{}, fmt.Errorf("horizon must be a positive whole sampling interval")
	}
	steps := int(request.Horizon / request.SamplingInterval)
	if len(samples) <= steps {
		return Prediction{}, fmt.Errorf("history has %d samples, need more than %d", len(samples), steps)
	}
	peaks := make([]float64, 0, len(samples)-steps)
	for start := 0; start+steps <= len(samples); start++ {
		peak := samples[start].ObservedValue
		for _, sample := range samples[start+1 : start+steps] {
			if sample.ObservedValue > peak {
				peak = sample.ObservedValue
			}
		}
		peaks = append(peaks, peak)
	}
	sort.Float64s(peaks)
	return Prediction{P50: quantile(peaks, 0.50), P95: quantile(peaks, operationalQuantile(request))}, nil
}

func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(q*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}
