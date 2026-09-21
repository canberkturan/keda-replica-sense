package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestScalerMetricsRecordsServedAndWithheldRequests(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewScalerMetrics(registry)
	metrics.RecordRequest("get_metrics", true, 10*time.Second)
	metrics.RecordRequest("get_metrics", false, 0)
	collected, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range collected {
		if family.GetName() == "replicasense_scaler_requests_total" && len(family.GetMetric()) == 2 {
			return
		}
	}
	t.Fatal("scaler metrics must distinguish served and withheld requests")
}
