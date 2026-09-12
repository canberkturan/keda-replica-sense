package forecast

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestWalkForwardUsesFutureHorizonMaximum(t *testing.T) {
	values := []float64{1, 2, 1, 2, 1, 2, 1, 2, 1, 2}
	samples := make([]domain.Sample, len(values))
	for i, value := range values {
		samples[i] = domain.Sample{ObservedAt: time.Unix(int64(i)*60, 0), ObservedValue: value}
	}
	metrics, err := WalkForward(SeasonalBaseline{}, samples, Request{Horizon: time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: 2 * time.Minute}, 0)
	if err != nil || metrics.Windows == 0 || metrics.Coverage != 1 || metrics.UnderpredictionRate != 0 || metrics.PinballLossP95 != 0 {
		t.Fatalf("WalkForward() = %#v, %v", metrics, err)
	}
}
