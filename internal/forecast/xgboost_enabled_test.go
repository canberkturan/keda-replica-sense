//go:build xgboost

package forecast

import (
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestXGBoostArtifactLoadsAndPredictsHorizonMaximum(t *testing.T) {
	interval := time.Minute
	request := Request{Horizon: interval, SamplingInterval: interval, SeasonalPeriod: 2 * interval}
	samples := make([]domain.Sample, 80)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range samples {
		value := float64(i%6) + float64(i)/20
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * interval), ObservedValue: value}
	}
	model, artifact, err := TrainXGBoost(samples, request)
	if err != nil {
		t.Fatal(err)
	}
	defer model.(*xgboostModel).Close()
	if len(artifact) == 0 {
		t.Fatal("empty xgboost artifact")
	}
	loaded, err := LoadModel(StoredModel{ID: "xgb-1", Engine: "xgboost", Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.(*xgboostModel).Close()
	prediction, err := loaded.PredictSamples(samples, request)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(prediction.P50) || math.IsNaN(prediction.P95) || prediction.P50 < 0 || prediction.P95 < prediction.P50 {
		t.Fatalf("invalid prediction: %#v", prediction)
	}
}
