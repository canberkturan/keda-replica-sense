package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHPARecoveryMetricsTrackMissingRestoredAndDeletedObjects(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewHPARecoveryMetrics(registry)
	m.RecordMissing("cluster", "default", "payments", 1)
	m.RecordOutcome("requested")
	m.RecordMissing("cluster", "default", "payments", 0)
	m.RecordOutcome("restored")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundMissing, foundOutcomes := false, false
	for _, family := range families {
		switch family.GetName() {
		case "replicasense_hpa_missing_predictive_metrics":
			foundMissing = len(family.Metric) == 1 && family.Metric[0].GetGauge().GetValue() == 0
		case "replicasense_hpa_recovery_total":
			foundOutcomes = len(family.Metric) == 2
		}
	}
	if !foundMissing || !foundOutcomes {
		t.Fatal("missing or incorrect recovery metrics")
	}
	m.Delete("cluster", "default", "payments")
	families, err = registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "replicasense_hpa_missing_predictive_metrics" {
			t.Fatal("deleted objects must not retain missing-metric series")
		}
	}
}
