package observability

import (
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

// SamplerMetrics exposes the source values persisted by the sampler. It is
// intentionally labelled with stable workload identity rather than the source
// query, which may contain high-cardinality user data.
type SamplerMetrics struct {
	observed *prometheus.GaugeVec
}

func NewSamplerMetrics(registerer prometheus.Registerer) *SamplerMetrics {
	m := &SamplerMetrics{observed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "replicasense_observed_demand",
		Help: "Latest source demand observed and persisted by ReplicaSense.",
	}, []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"})}
	registerer.MustRegister(m.observed)
	return m
}

func (m *SamplerMetrics) RecordSample(workload workloadstore.SamplingWorkload, value float64) {
	if m == nil {
		return
	}
	key := workload.Spec.Key
	m.observed.WithLabelValues(key.ClusterID, key.Namespace, key.ScaledObjectName, key.PredictiveTriggerName).Set(value)
}
