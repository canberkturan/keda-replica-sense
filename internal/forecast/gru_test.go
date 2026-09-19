package forecast

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestGRUArtifactRoundTripPredictsHorizonMaximum(t *testing.T) {
	samples := gruTestSamples(180)
	request := Request{Horizon: 5 * time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: 24 * time.Hour, Quantile: .95, BusinessLocation: time.FixedZone("business", 3*60*60)}
	model, artifact, err := TrainGRU(samples, request)
	if err != nil {
		t.Fatal(err)
	}
	if model.Engine() != "gru" || len(artifact) == 0 {
		t.Fatalf("trained model=%T artifact=%d", model, len(artifact))
	}
	loaded, err := LoadModel(StoredModel{ID: "gru-1", Engine: "gru", Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	prediction, err := loaded.PredictSamples(samples, request)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(prediction.P50) || math.IsNaN(prediction.P95) || math.IsInf(prediction.P95, 0) || prediction.P50 < 0 || prediction.P95 < prediction.P50 {
		t.Fatalf("invalid GRU prediction %#v", prediction)
	}
	metrics, err := ValidateGRU(samples, request, 2)
	if err != nil || metrics.Windows != 2 || math.IsNaN(metrics.PinballLossP95) || math.IsInf(metrics.MeanAbsoluteError, 0) {
		t.Fatalf("invalid GRU walk-forward metrics %#v, %v", metrics, err)
	}
}

func TestGRUArtifactRejectsWrongWeightShape(t *testing.T) {
	artifact := gruArtifact{Engine: "gru", Format: gruFormat, FeatureSchema: GRUFeatureSchema, UpperQuantile: .95, Sequence: gruSequenceLength, Inputs: gruInputSize, Hidden: gruHiddenSize, ValueScale: 1, P50: zeroGRUWeights(gruInputSize, gruHiddenSize), P95: zeroGRUWeights(gruInputSize, gruHiddenSize)}
	artifact.P95.Output = artifact.P95.Output[:len(artifact.P95.Output)-1]
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModel(StoredModel{ID: "invalid", Engine: "gru", Artifact: encoded}); err == nil {
		t.Fatal("invalid GRU weight shape must be rejected")
	}
}

func TestGRUCalendarInputIncludesBusinessDayAndClock(t *testing.T) {
	location := time.FixedZone("business", 3*60*60)
	input := gruInput(domain.Sample{ObservedAt: time.Date(2026, time.September, 20, 9, 30, 0, 0, time.UTC), ObservedValue: 15}, 5, 10, location)
	if len(input) != gruInputSize || input[0] != 1 || input[9] != 1 {
		t.Fatalf("unexpected GRU input %#v", input)
	}
	if input[1] == 0 && input[2] == 0 || input[3] == 0 && input[4] == 0 || input[5] == 0 && input[6] == 0 || input[7] == 0 && input[8] == 0 {
		t.Fatalf("clock, weekday, day-of-month, and month features must be present: %#v", input)
	}
}

func gruTestSamples(count int) []domain.Sample {
	start := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	samples := make([]domain.Sample, count)
	for i := range samples {
		cycle := float64(i % 30)
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: 20 + cycle + float64((i/30)%3)*3}
	}
	return samples
}
