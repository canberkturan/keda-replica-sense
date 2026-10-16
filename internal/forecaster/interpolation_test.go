package forecaster

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/dataset"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type fixedLookbackModel struct {
	lookback int
	demand   float64
	received []domain.Sample
}

func (m *fixedLookbackModel) Engine() string         { return "test" }
func (m *fixedLookbackModel) InferenceLookback() int { return m.lookback }
func (m *fixedLookbackModel) PredictSamples(values []domain.Sample, _ forecast.Request) (forecast.Prediction, error) {
	m.received = append([]domain.Sample(nil), values...)
	return forecast.Prediction{P50: m.demand / 2, P95: m.demand}, nil
}

func interpolationFixture(lookback int, missing map[int]bool) (Service, workloadstore.SamplingWorkload, *fixedLookbackModel, []domain.Sample, time.Time) {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	var values []domain.Sample
	for index := 0; index < lookback+10; index++ {
		if !missing[index] {
			values = append(values, domain.Sample{ObservedAt: start.Add(time.Duration(index) * time.Minute), ObservedValue: 100})
		}
	}
	model := &fixedLookbackModel{lookback: lookback, demand: 10000}
	workload := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab"}, Source: domain.PrometheusSource{Threshold: 500}, Bounds: domain.ReplicaBounds{Min: 1, Max: 10}, Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 30 * 24 * time.Hour, Horizon: 10 * time.Minute}}}
	service := Service{Samples: samples{values}, Snapshots: &snapshots{}, SeasonalPeriod: 24 * time.Hour, Models: staticModels{model: model}, CurrentReplicas: func(context.Context, workloadstore.SamplingWorkload) (float64, error) { return 4, nil }}
	return service, workload, model, values, start.Add(time.Duration(lookback+10) * time.Minute)
}

func TestTrainedModelRecoversShortGapWithoutSeasonalWarmupOrObservedWrites(t *testing.T) {
	for _, lookback := range []int{61, 120} {
		service, workload, model, observed, now := interpolationFixture(lookback, map[int]bool{lookback + 5: true, lookback + 6: true})
		original := append([]domain.Sample(nil), observed...)
		count := -1
		service.OnInputQuality = func(_ workloadstore.SamplingWorkload, estimated int) { count = estimated }
		got, err := service.Forecast(context.Background(), workload, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(model.received) != lookback || count != 2 || got.SafeDemand != 5 || got.SurgeDemand != 0 || !strings.Contains(got.SafetyReason, "inference_interpolated_points=2;conservative_gap_recovery") {
			t.Fatalf("lookback=%d: got %#v; inputs=%d estimated=%d", lookback, got, len(model.received), count)
		}
		if !reflect.DeepEqual(observed, original) || !got.ObservedAt.Equal(original[len(original)-1].ObservedAt) {
			t.Fatal("observed samples/freshness must remain entirely real")
		}
		if err := dataset.Validate(original, dataset.QualityPolicy{SamplingInterval: time.Minute, MaxGapIntervals: 2}); err == nil {
			t.Fatal("training data must still contain the actual gap; it must not be repaired by inference")
		}
	}
}

func TestInterpolatedInferenceCannotDownscaleOrBypassCapacityGuard(t *testing.T) {
	service, workload, model, _, now := interpolationFixture(61, map[int]bool{66: true, 67: true})
	model.demand = 1
	got, err := service.Forecast(context.Background(), workload, now)
	if err != nil || got.SafeDemand != 4 {
		t.Fatalf("estimated inputs must not request downscale: %#v, %v", got, err)
	}
	model.demand = 10000
	service.PredictiveAllowed = func(context.Context, workloadstore.SamplingWorkload, float64, float64) bool { return false }
	got, err = service.Forecast(context.Background(), workload, now)
	if err != nil || got.SafeDemand != 4 || !strings.Contains(got.SafetyReason, "capacity_unavailable") {
		t.Fatalf("normal capacity protections must remain active: %#v, %v", got, err)
	}
}

func TestInterpolatedInferenceWithholdsLongGapsStaleDataAndUnknownReplicas(t *testing.T) {
	for _, test := range []struct {
		name    string
		missing map[int]bool
		mutate  func(*Service, *time.Time)
		want    string
	}{
		{"long gap", map[int]bool{60: true, 61: true, 62: true, 63: true, 64: true, 65: true}, func(*Service, *time.Time) {}, "insufficient_history"},
		{"stale input", map[int]bool{66: true, 67: true}, func(_ *Service, now *time.Time) { *now = now.Add(10 * time.Minute) }, "stale_source"},
		{"unknown current replicas", map[int]bool{66: true, 67: true}, func(service *Service, _ *time.Time) { service.CurrentReplicas = nil }, "cluster_error"},
		{"interpolation disabled", map[int]bool{66: true, 67: true}, func(service *Service, _ *time.Time) { service.DisableShortGapInterpolation = true }, "insufficient_history"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, workload, _, _, now := interpolationFixture(61, test.missing)
			test.mutate(&service, &now)
			gotReason := ""
			service.OnUnavailable = func(_ workloadstore.SamplingWorkload, reason string) { gotReason = reason }
			if _, err := service.Forecast(context.Background(), workload, now); err == nil || gotReason != test.want {
				t.Fatalf("unsafe forecast must be withheld: err=%v reason=%q, want %q", err, gotReason, test.want)
			}
			if service.Snapshots.(*snapshots).s.GeneratedAt != (time.Time{}) {
				t.Fatal("withheld forecast must not create a new snapshot")
			}
		})
	}
}
