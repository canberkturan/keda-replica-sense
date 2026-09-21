package observability

import (
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
	"github.com/prometheus/client_golang/prometheus"
)

// ScaledObjectTriggerMetrics exports one bounded configuration-info series for
// every trigger belonging to a ReplicaSense-managed ScaledObject. Query text is
// intentionally represented by the source fingerprint, rather than being
// exported as a Prometheus label: raw PromQL can contain sensitive label values
// and each edit would create unbounded label cardinality.
type ScaledObjectTriggerMetrics struct {
	info *prometheus.GaugeVec
}

func NewScaledObjectTriggerMetrics(registerer prometheus.Registerer) *ScaledObjectTriggerMetrics {
	m := &ScaledObjectTriggerMetrics{info: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "replicasense_scaledobject_trigger_info",
		Help: "ReplicaSense-managed ScaledObject trigger configuration; value is always 1.",
	}, []string{
		"cluster_id", "scaled_object_namespace", "scaled_object", "trigger_name", "trigger_type", "trigger_role",
		"linked_predictive_trigger", "source_trigger", "reactive_threshold", "source_fingerprint", "policy_revision",
		"model_engine", "quantile", "forecast_horizon", "training_window", "sampling_interval", "startup_latency",
		"safety_buffer", "business_timezone", "immediate_training", "clear_old_models", "configuration_valid",
	})}
	registerer.MustRegister(m.info)
	return m
}

// Record replaces the previous configuration rows for one ScaledObject.
func (m *ScaledObjectTriggerMetrics) Record(clusterID, namespace, scaledObject string, triggers []scaledobject.TriggerInput, result scaledobject.ParseResult) {
	if m == nil {
		return
	}
	base := prometheus.Labels{"cluster_id": clusterID, "scaled_object_namespace": namespace, "scaled_object": scaledObject}
	m.info.DeletePartialMatch(base)
	if len(result.Candidates) == 0 {
		return
	}

	predictive := make(map[string]scaledobject.Candidate, len(result.Candidates))
	sourceToPredictive := make(map[string]scaledobject.Candidate, len(result.Candidates))
	for _, candidate := range result.Candidates {
		predictive[candidate.PredictiveTriggerName] = candidate
		if candidate.Spec != nil {
			sourceToPredictive[candidate.Spec.Source.TriggerName] = candidate
		}
	}

	for _, trigger := range triggers {
		labels := triggerLabels(base, trigger)
		if candidate, ok := predictive[trigger.Name]; ok {
			labels["trigger_role"] = "predictive"
			labels["configuration_valid"] = strconv.FormatBool(candidate.Spec != nil)
			if candidate.Spec != nil {
				applyPredictiveLabels(labels, candidate.Spec)
			}
		} else if candidate, ok := sourceToPredictive[trigger.Name]; ok && candidate.Spec != nil {
			labels["trigger_role"] = "reactive_source"
			labels["linked_predictive_trigger"] = candidate.PredictiveTriggerName
			labels["source_fingerprint"] = candidate.Spec.SourceFingerprint
			labels["reactive_threshold"] = strconv.FormatFloat(candidate.Spec.Source.Threshold, 'f', -1, 64)
			labels["configuration_valid"] = "true"
		} else {
			labels["trigger_role"] = "other"
			labels["configuration_valid"] = "true"
		}
		m.info.With(labels).Set(1)
	}
}

func (m *ScaledObjectTriggerMetrics) Delete(clusterID, namespace, scaledObject string) {
	if m == nil {
		return
	}
	m.info.DeletePartialMatch(prometheus.Labels{"cluster_id": clusterID, "scaled_object_namespace": namespace, "scaled_object": scaledObject})
}

func triggerLabels(base prometheus.Labels, trigger scaledobject.TriggerInput) prometheus.Labels {
	labels := make(prometheus.Labels, len(base)+19)
	for key, value := range base {
		labels[key] = value
	}
	labels["trigger_name"] = trigger.Name
	labels["trigger_type"] = trigger.Type
	for _, key := range []string{"trigger_role", "linked_predictive_trigger", "source_trigger", "reactive_threshold", "source_fingerprint", "policy_revision", "model_engine", "quantile", "forecast_horizon", "training_window", "sampling_interval", "startup_latency", "safety_buffer", "business_timezone", "immediate_training", "clear_old_models", "configuration_valid"} {
		labels[key] = ""
	}
	return labels
}

func applyPredictiveLabels(labels prometheus.Labels, spec *domain.WorkloadSpec) {
	forecast := spec.Forecast
	labels["source_trigger"] = spec.Source.TriggerName
	labels["source_fingerprint"] = spec.SourceFingerprint
	labels["policy_revision"] = spec.PolicyRevision
	labels["model_engine"] = forecast.ModelEngine
	labels["quantile"] = strconv.FormatFloat(forecast.Quantile, 'f', -1, 64)
	labels["forecast_horizon"] = formatDuration(forecast.Horizon)
	labels["training_window"] = formatDuration(forecast.TrainingWindow)
	labels["sampling_interval"] = formatDuration(forecast.SamplingInterval)
	labels["startup_latency"] = formatDuration(forecast.StartupLatency)
	labels["safety_buffer"] = formatDuration(forecast.SafetyBuffer)
	labels["business_timezone"] = forecast.BusinessTimezone
	labels["immediate_training"] = strconv.FormatBool(forecast.ImmediateTraining)
	labels["clear_old_models"] = strconv.FormatBool(forecast.ClearOldModels)
}

func formatDuration(value time.Duration) string {
	if value == 0 {
		return "0s"
	}
	if value%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(value/(24*time.Hour)), 10) + "d"
	}
	if value%time.Hour == 0 {
		return strconv.FormatInt(int64(value/time.Hour), 10) + "h"
	}
	if value%time.Minute == 0 {
		return strconv.FormatInt(int64(value/time.Minute), 10) + "m"
	}
	return value.String()
}
