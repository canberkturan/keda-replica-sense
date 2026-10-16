//go:build xgboost

package forecast

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/dataset"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestXGBoostInferenceLookbackIsIndependentOfTrainingWindow(t *testing.T) {
	if got := (&xgboostModel{}).InferenceLookback(); got != 61 {
		t.Fatalf("inference needs the current point plus 60 lags, got %d", got)
	}
}

func TestNativeXGBoostConsumesBoundedInterpolatedInputs(t *testing.T) {
	request := Request{Horizon: time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: 2 * time.Minute, Quantile: .97}
	values := make([]domain.Sample, 80)
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for index := range values {
		values[index] = domain.Sample{ObservedAt: start.Add(time.Duration(index) * time.Minute), ObservedValue: 100 + float64(index%6)}
	}
	model, _, err := TrainXGBoost(values, request)
	if err != nil {
		t.Fatal(err)
	}
	defer model.(*xgboostModel).Close()
	var observed []domain.Sample
	for index, sample := range values {
		if index != 74 && index != 75 {
			observed = append(observed, sample)
		}
	}
	input, err := dataset.PrepareInferenceHistory(observed, dataset.InterpolationPolicy{SamplingInterval: time.Minute, Lookback: model.(InferenceLookbackModel).InferenceLookback()})
	if err != nil || input.EstimatedCount != 2 || len(input.Samples) != 61 {
		t.Fatalf("native recovery input: estimated=%d samples=%d err=%v", input.EstimatedCount, len(input.Samples), err)
	}
	prediction, err := model.PredictSamples(input.Samples, request)
	if err != nil || math.IsNaN(prediction.P95) || math.IsInf(prediction.P95, 0) || prediction.P95 < 0 {
		t.Fatalf("recovered native XGBoost inference: %#v, %v", prediction, err)
	}
}

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
