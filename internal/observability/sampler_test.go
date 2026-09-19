package observability

import (
	"testing"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSamplerMetricsRecordsObservedDemand(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewSamplerMetrics(registry)
	metrics.RecordSample(workloadstore.SamplingWorkload{Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "lab", Namespace: "demo", ScaledObjectName: "api", PredictiveTriggerName: "predictive"}}}, 42)
	collected, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range collected {
		if family.GetName() == "replicasense_observed_demand" && family.GetMetric()[0].GetGauge().GetValue() == 42 {
			return
		}
	}
	t.Fatalf("unexpected observed-demand metric: %#v", collected)
}
