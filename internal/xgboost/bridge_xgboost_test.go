//go:build xgboost

package xgboost

import (
	"math"
	"testing"
)

func TestVersion(t *testing.T) {
	version, err := Version()
	if err != nil {
		t.Fatal(err)
	}
	if version != "2.1.4" {
		t.Fatalf("Version() = %q, want 2.1.4", version)
	}
}

func TestTrainPredictArtifactRoundTrip(t *testing.T) {
	features := []float32{0, 1, 2, 3, 4, 5, 6, 7}
	labels := []float32{0, 2, 4, 6, 8, 10, 12, 14}
	model, err := Train(features, labels, len(labels), 1, TrainOptions{Rounds: 30, MaxDepth: 2, Eta: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	predictions, err := model.Predict([]float32{1, 6}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(predictions) != 2 || math.IsNaN(float64(predictions[0])) || predictions[1] <= predictions[0] {
		t.Fatalf("unexpected predictions: %v", predictions)
	}
	artifact, err := model.Artifact()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	loadedPredictions, err := reloaded.Predict([]float32{1, 6}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := range predictions {
		if math.Abs(float64(predictions[i]-loadedPredictions[i])) > 0.00001 {
			t.Fatalf("prediction %d changed after artifact round trip: %v != %v", i, predictions[i], loadedPredictions[i])
		}
	}
}

func TestTrainQuantileObjective(t *testing.T) {
	features := make([]float32, 100)
	labels := make([]float32, 100)
	for i := 95; i < len(labels); i++ {
		labels[i] = 100
	}
	model, err := Train(features, labels, len(labels), 1, TrainOptions{Rounds: 100, MaxDepth: 2, Eta: 0.2, Objective: "reg:quantileerror", QuantileAlpha: .95})
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	predictions, err := model.Predict([]float32{0}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(predictions) != 1 || math.IsNaN(float64(predictions[0])) || math.IsInf(float64(predictions[0]), 0) {
		t.Fatalf("invalid quantile prediction: %v", predictions)
	}
	if predictions[0] < 80 {
		t.Fatalf("P95 prediction = %v, want an upper-tail value", predictions[0])
	}
}

func TestArtifactRoundTrip(t *testing.T) {
	model, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	if err := model.SetParam("num_feature", "1"); err != nil {
		t.Fatal(err)
	}
	artifact, err := model.Artifact()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Close()
}
