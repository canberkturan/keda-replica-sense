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
