package observability

import "github.com/prometheus/client_golang/prometheus"

type HPARecoveryMetrics struct {
	missing  *prometheus.GaugeVec
	outcomes *prometheus.CounterVec
}

func NewHPARecoveryMetrics(registerer prometheus.Registerer) *HPARecoveryMetrics {
	m := &HPARecoveryMetrics{
		missing: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "replicasense_hpa_missing_predictive_metrics",
			Help: "Number of configured ReplicaSense trigger metrics missing from a managed HPA; excludes paused and composite-metric ScaledObjects.",
		}, []string{"cluster_id", "scaled_object_namespace", "scaled_object"}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "replicasense_hpa_recovery_total",
			Help: "Predictive HPA recovery outcomes: requested, restored, probe_failed, or patch_failed.",
		}, []string{"outcome"}),
	}
	registerer.MustRegister(m.missing, m.outcomes)
	return m
}

func (m *HPARecoveryMetrics) RecordMissing(clusterID, namespace, scaledObject string, count int) {
	m.missing.WithLabelValues(clusterID, namespace, scaledObject).Set(float64(count))
}

func (m *HPARecoveryMetrics) RecordOutcome(outcome string) {
	m.outcomes.WithLabelValues(outcome).Inc()
}

func (m *HPARecoveryMetrics) Delete(clusterID, namespace, scaledObject string) {
	m.missing.DeleteLabelValues(clusterID, namespace, scaledObject)
}
