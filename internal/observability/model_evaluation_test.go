package observability

import (
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
	"testing"
	"time"
)

func TestModelEvaluationMetricsReplaceOldScoresAndAvoidArtifactLabels(t *testing.T) {
	r := prometheus.NewRegistry()
	m := NewModelEvaluationMetrics(r)
	w := workloadstore.SamplingWorkload{Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "test", Namespace: "test", ScaledObjectName: "test", PredictiveTriggerName: "predictive"}}}
	report := training.EvaluationReport{At: time.Now(), Action: "shadow", Reason: "paired_evaluation_passed", ExpectedActiveID: "private-model-id", Evidence: forecast.Comparison{Metrics: map[string]forecast.ValidationMetrics{"candidate": {Windows: 100, Coverage: .97}}}}
	m.Record(w, report)
	report.Action = "withheld"
	report.Reason = "stale_source"
	report.Evidence.Metrics = nil
	m.Record(w, report)
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "replicasense_model_evaluation_score" {
			t.Fatal("stale model scores retained")
		}
		if family.GetName() == "replicasense_model_evaluation_status" && len(family.Metric) != 1 {
			t.Fatal("old evaluation states retained")
		}
		for _, point := range family.Metric {
			for _, label := range point.Label {
				if label.GetValue() == "private-model-id" {
					t.Fatal("artifact ID leaked into metric labels")
				}
			}
		}
	}
}
