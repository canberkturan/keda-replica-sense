package forecast

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

type fixedUpperModel struct{ upper float64 }

func (m fixedUpperModel) Engine() string { return "fixed" }
func (m fixedUpperModel) PredictSamples([]domain.Sample, Request) (Prediction, error) {
	return Prediction{P50: m.upper, P95: m.upper}, nil
}

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

func TestWalkForwardUsesConfiguredOperationalQuantileForPinballLoss(t *testing.T) {
	samples := []domain.Sample{{ObservedValue: 1}, {ObservedValue: 1}, {ObservedValue: 2}, {ObservedValue: 2}}
	metrics, err := WalkForward(fixedUpperModel{upper: 1}, samples, Request{Horizon: time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: time.Minute, Quantile: .99}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.PinballLossP95 != .99 {
		t.Fatalf("pinball loss = %v, want .99", metrics.PinballLossP95)
	}
}
