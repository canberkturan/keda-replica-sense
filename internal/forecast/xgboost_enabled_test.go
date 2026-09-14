//go:build xgboost

package forecast

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestXGBoostArtifactLoadsAndPredictsHorizonMaximum(t *testing.T) {
	interval := time.Minute
	request := Request{Horizon: interval, SamplingInterval: interval, SeasonalPeriod: 2 * interval, Quantile: .99}
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
	var stored xgboostArtifact
	if err := json.Unmarshal(artifact, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.UpperQuantile != .99 || stored.FeatureSchema != xgboostFeatureSchema {
		t.Fatalf("artifact quantile/schema = %#v", stored)
	}
	loaded, err := LoadModel(StoredModel{ID: "xgb-1", Engine: "xgboost", Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.(*xgboostModel).Close()
	if got := loaded.(*xgboostModel).OperationalQuantile(); got != .99 {
		t.Fatalf("loaded operational quantile = %v, want .99", got)
	}
	prediction, err := loaded.PredictSamples(samples, request)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(prediction.P50) || math.IsNaN(prediction.P95) || prediction.P50 < 0 || prediction.P95 < prediction.P50 {
		t.Fatalf("invalid prediction: %#v", prediction)
	}
}

func TestXGBoostUsesQuantileObjectivesForMedianAndConfiguredUpperBound(t *testing.T) {
	p50, upper := xgboostTrainOptions(Request{Quantile: .99})
	if p50.Objective != "reg:quantileerror" || p50.QuantileAlpha != .50 {
		t.Fatalf("P50 options = %#v", p50)
	}
	if upper.Objective != "reg:quantileerror" || upper.QuantileAlpha != .99 {
		t.Fatalf("upper options = %#v", upper)
	}
}

func TestXGBoostRejectsV2ArtifactSchema(t *testing.T) {
	legacy, err := json.Marshal(xgboostArtifact{Engine: "xgboost", Format: "replicasense-xgboost-v2", FeatureSchema: "demand-calendar-lag-v2", UpperQuantile: .95, P50Artifact: "x", P95Artifact: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(StoredModel{ID: "legacy", Engine: "xgboost", Artifact: legacy}); err == nil {
		t.Fatal("legacy V2 artifact must fail safely")
	}
}
