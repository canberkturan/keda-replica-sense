package observability

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/kube"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

func TestForecasterMetricsUsesConfiguredOperationalQuantileLabel(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewForecasterMetrics(registry)
	workload := workloadstore.SamplingWorkload{Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab", Namespace: "demo", ScaledObjectName: "api", PredictiveTriggerName: "predictive"}, Forecast: domain.ForecastConfig{Quantile: .99}}}
	metrics.Record(workload, domain.ForecastSnapshot{GeneratedAt: time.Now(), ForecastP50: 10, ForecastP95: 20}, time.Now())
	collected, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, family := range collected {
		if family.GetName() != "replicasense_forecast_demand" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "quantile" && label.GetValue() == "0.99" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("configured 0.99 quantile label was not exported")
	}
}

func TestForecasterMetricsExportsClusterCapacity(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewForecasterMetrics(registry)
	metrics.RecordCapacity("lab", kube.CapacitySnapshot{})
	collected, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range collected {
		if family.GetName() == "replicasense_cluster_resource_capacity" && len(family.GetMetric()) == 8 {
			return
		}
	}
	t.Fatal("cluster capacity metric must expose CPU and memory accounting states")
}

func TestForecasterMetricsExplainInterpolationAndClearWithheldGuesses(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewForecasterMetrics(registry)
	workload := workloadstore.SamplingWorkload{Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab", Namespace: "demo", ScaledObjectName: "api", PredictiveTriggerName: "predictive"}}}
	metrics.RecordInputQuality(workload, 2)
	metrics.Record(workload, domain.ForecastSnapshot{GeneratedAt: time.Now(), ForecastP95: 100, SafetyReason: "baseline_replicas;inference_interpolated_points=2;conservative_gap_recovery"}, time.Now())
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "replicasense_forecast_input_estimated_samples" {
			found = family.Metric[0].GetGauge().GetValue() == 2
		}
	}
	if !found {
		t.Fatal("estimated inference inputs must be visible")
	}
	metrics.RecordUnavailable(workload, "insufficient_history")
	families, err = registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, family := range families {
		if family.GetName() == "replicasense_forecast_demand" {
			t.Fatal("withholding must remove frozen forecast guesses")
		}
		if family.GetName() == "replicasense_forecast_ready" && family.Metric[0].GetGauge().GetValue() != 0 {
			t.Fatal("withheld forecasts must not be ready")
		}
		if family.GetName() == "replicasense_forecast_status" {
			if len(family.Metric) != 1 {
				t.Fatal("obsolete status labels must be deleted")
			}
			for _, label := range family.Metric[0].Label {
				if label.GetName() == "reason" && label.GetValue() == "insufficient_history" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("withholding reason must be exported")
	}
}
