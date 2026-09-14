package forecaster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestModelCacheRetainsLastGoodRefresh(t *testing.T) {
	reader := &fakeActiveModels{models: []forecast.StoredModel{{ID: "m1", WorkloadID: "w1", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`), ActivatedAt: time.Now()}}}
	cache := NewModelCache("lab", reader, time.Hour)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{SourceFingerprint: "fp"}}
	if model, ok := cache.ModelFor(w); !ok || model.Engine() != "seasonal-baseline" {
		t.Fatalf("ModelFor() = %#v, %t", model, ok)
	}
	reader.err = errors.New("database down")
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil")
	}
	if _, ok := cache.ModelFor(w); !ok {
		t.Fatal("failed refresh discarded known model")
	}
}

func TestModelCacheRejectsExpiredActiveModel(t *testing.T) {
	reader := &fakeActiveModels{models: []forecast.StoredModel{{ID: "m1", WorkloadID: "w1", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`), ActivatedAt: time.Now().Add(-2 * time.Hour)}}}
	cache := NewModelCache("lab", reader, time.Hour)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.ModelFor(workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{SourceFingerprint: "fp"}}); ok {
		t.Fatal("expired model must not be served")
	}
}

func TestModelCacheRejectsArtifactForDifferentConfiguredEngine(t *testing.T) {
	reader := &fakeActiveModels{models: []forecast.StoredModel{{ID: "m1", WorkloadID: "w1", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`), ActivatedAt: time.Now()}}}
	cache := NewModelCache("lab", reader, time.Hour)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{SourceFingerprint: "fp", Forecast: domain.ForecastConfig{ModelEngine: "rolling-quantile"}}}
	if _, ok := cache.ModelFor(w); ok {
		t.Fatal("mismatched active model must not override configured engine")
	}
}

func TestModelCacheKeepsDeserializedModelForUnchangedArtifact(t *testing.T) {
	reader := &fakeActiveModels{models: []forecast.StoredModel{{ID: "m1", WorkloadID: "w1", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`), ActivatedAt: time.Now()}}}
	cache := NewModelCache("lab", reader, time.Hour)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	workload := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{SourceFingerprint: "fp"}}
	first, ok := cache.ModelFor(workload)
	if !ok {
		t.Fatal("missing first model")
	}
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, ok := cache.ModelFor(workload)
	if !ok || first != second {
		t.Fatal("unchanged artifact was deserialized again")
	}
}

func TestModelCacheLoadsValidModelsWhenOneArtifactIsInvalid(t *testing.T) {
	reader := &fakeActiveModels{models: []forecast.StoredModel{
		{ID: "bad", WorkloadID: "bad", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`not-json`), ActivatedAt: time.Now()},
		{ID: "good", WorkloadID: "good", SourceFingerprint: "fp", Engine: "seasonal-baseline", Artifact: []byte(`{"engine":"seasonal-baseline"}`), ActivatedAt: time.Now()},
	}}
	cache := NewModelCache("lab", reader, time.Hour)
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil for corrupt artifact")
	}
	if _, ok := cache.ModelFor(workloadstore.SamplingWorkload{ID: "bad", Spec: domain.WorkloadSpec{SourceFingerprint: "fp"}}); ok {
		t.Fatal("corrupt artifact must not be served")
	}
	if model, ok := cache.ModelFor(workloadstore.SamplingWorkload{ID: "good", Spec: domain.WorkloadSpec{SourceFingerprint: "fp"}}); !ok || model.Engine() != "seasonal-baseline" {
		t.Fatalf("valid model missing: %#v, %t", model, ok)
	}
}

func TestModelCacheRejectsArtifactForDifferentConfiguredQuantile(t *testing.T) {
	cache := NewModelCache("lab", &fakeActiveModels{}, time.Hour)
	cache.models["w1:fp"] = cachedModel{id: "m1", model: fakeQuantileModel{quantile: .95}, activatedAt: time.Now()}
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{SourceFingerprint: "fp", Forecast: domain.ForecastConfig{Quantile: .99}}}
	if _, ok := cache.ModelFor(w); ok {
		t.Fatal("artifact with a different configured quantile must fail closed")
	}
}

type fakeQuantileModel struct{ quantile float64 }

func (m fakeQuantileModel) Engine() string               { return "xgboost" }
func (m fakeQuantileModel) OperationalQuantile() float64 { return m.quantile }
func (m fakeQuantileModel) PredictSamples([]domain.Sample, forecast.Request) (forecast.Prediction, error) {
	return forecast.Prediction{}, nil
}

type fakeActiveModels struct {
	models []forecast.StoredModel
	err    error
}

func (f *fakeActiveModels) ListActiveModels(context.Context, string) ([]forecast.StoredModel, error) {
	return f.models, f.err
}
