package forecast

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestResolveModelSeasonalBaseline(t *testing.T) {
	model, err := ResolveModel("seasonal-baseline")
	if err != nil || model.Engine() != "seasonal-baseline" {
		t.Fatalf("ResolveModel() = %#v, %v", model, err)
	}
	prediction, err := model.PredictSamples([]domain.Sample{{ObservedValue: 1}, {ObservedValue: 5}}, Request{Horizon: time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: time.Minute})
	if err != nil || prediction.P50 != 5 || prediction.P95 != 5 {
		t.Fatalf("PredictSamples() = %#v, %v", prediction, err)
	}
}

func TestResolveModelRejectsUnknownEngine(t *testing.T) {
	if _, err := ResolveModel("lightgbm"); err == nil {
		t.Fatal("ResolveModel(lightgbm) error = nil")
	}
}

func TestRollingQuantileProducesDistinctQuantiles(t *testing.T) {
	samples := []domain.Sample{{ObservedValue: 1}, {ObservedValue: 1}, {ObservedValue: 2}, {ObservedValue: 2}, {ObservedValue: 10}}
	prediction, err := (RollingQuantile{}).PredictSamples(samples, Request{Horizon: time.Minute, SamplingInterval: time.Minute})
	if err != nil || prediction.P50 != 2 || prediction.P95 != 10 {
		t.Fatalf("PredictSamples() = %#v, %v", prediction, err)
	}
}

func TestHoltWintersCapturesSeasonalPeak(t *testing.T) {
	values := []float64{1, 4, 2, 5, 2, 5, 3, 6}
	samples := make([]domain.Sample, len(values))
	for i, value := range values {
		samples[i] = domain.Sample{ObservedValue: value}
	}
	prediction, err := (HoltWinters{}).PredictSamples(samples, Request{Horizon: 2 * time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: 2 * time.Minute})
	if err != nil || prediction.P50 <= 4 || prediction.P95 != prediction.P50 {
		t.Fatalf("PredictSamples() = %#v, %v", prediction, err)
	}
}

func TestLoadModelValidatesArtifactEngine(t *testing.T) {
	model, err := LoadModel(StoredModel{ID: "m1", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`)})
	if err != nil || model.Engine() != "seasonal-baseline" {
		t.Fatalf("LoadModel() = %#v, %v", model, err)
	}
	if _, err := LoadModel(StoredModel{ID: "m1", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"xgboost"}`)}); err == nil {
		t.Fatal("LoadModel() error = nil for mismatched engine")
	}
}
