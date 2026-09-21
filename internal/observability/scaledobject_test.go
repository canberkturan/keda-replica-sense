package observability

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestScaledObjectTriggerMetricsExportsReactiveAndPredictiveConfiguration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewScaledObjectTriggerMetrics(registry)
	spec := &domain.WorkloadSpec{
		Key:               domain.WorkloadKey{ClusterID: "cluster-a", Namespace: "shop", ScaledObjectName: "checkout", PredictiveTriggerName: "predictive"},
		Source:            domain.PrometheusSource{TriggerName: "reactive", Threshold: 500},
		SourceFingerprint: "source-hash",
		PolicyRevision:    "policy-hash",
		Forecast:          domain.ForecastConfig{ModelEngine: "gru", Quantile: .97, Horizon: 10 * time.Minute, TrainingWindow: 30 * 24 * time.Hour, SamplingInterval: time.Minute, StartupLatency: time.Minute, SafetyBuffer: time.Minute, BusinessTimezone: "UTC", ImmediateTraining: true, ClearOldModels: true},
	}
	metrics.Record("cluster-a", "shop", "checkout", []scaledobject.TriggerInput{
		{Type: "prometheus", Name: "reactive", Metadata: map[string]string{"query": "rate(http_requests_total[1m])"}},
		{Type: "external", Name: "predictive", Metadata: map[string]string{"sourceTrigger": "reactive"}},
	}, scaledobject.ParseResult{Candidates: []scaledobject.Candidate{{PredictiveTriggerName: "predictive", Spec: spec}}})

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || len(families[0].Metric) != 2 {
		t.Fatalf("got %#v, want one metric family with reactive and predictive rows", families)
	}
	labels := labelsOf(families[0].Metric[0])
	if labels["trigger_role"] != "predictive" {
		labels = labelsOf(families[0].Metric[1])
	}
	if labels["trigger_role"] != "predictive" || labels["model_engine"] != "gru" || labels["quantile"] != "0.97" || labels["forecast_horizon"] != "10m" || labels["training_window"] != "30d" || labels["immediate_training"] != "true" {
		t.Fatalf("unexpected predictive labels: %#v", labels)
	}
	if _, found := labels["query"]; found {
		t.Fatal("raw PromQL must not be exported as a metric label")
	}
}

func TestScaledObjectTriggerMetricsDeletesOldConfiguration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewScaledObjectTriggerMetrics(registry)
	metrics.Record("cluster-a", "shop", "checkout", []scaledobject.TriggerInput{{Type: "external", Name: "predictive"}}, scaledobject.ParseResult{Candidates: []scaledobject.Candidate{{PredictiveTriggerName: "predictive"}}})
	metrics.Delete("cluster-a", "shop", "checkout")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("got %#v after delete, want no configuration series", families)
	}
}

func labelsOf(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.Label))
	for _, label := range metric.Label {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
