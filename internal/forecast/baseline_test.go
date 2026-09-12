package forecast

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestSeasonalNaivePredictsFutureHorizonPeak(t *testing.T) {
	model := SeasonalNaive{PeriodSteps: 4}
	got, err := model.PredictHorizonMax([]float64{1, 2, 3, 4, 10, 20, 30, 40}, 3)
	if err != nil || got != 30 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPredictSamples(t *testing.T) {
	samples := []domain.Sample{{ObservedValue: 1}, {ObservedValue: 2}, {ObservedValue: 10}, {ObservedValue: 20}}
	got, err := SeasonalNaive{}.PredictSamples(samples, Request{Horizon: time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: 2 * time.Minute})
	if err != nil || got != 10 {
		t.Fatalf("got %v, %v", got, err)
	}
}
