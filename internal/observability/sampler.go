package observability

import (
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

// SamplerMetrics exposes the source values persisted by the sampler. It is
// intentionally labelled with stable workload identity rather than the source
// query, which may contain high-cardinality user data.
type SamplerMetrics struct {
	observed      *prometheus.GaugeVec
	cycles        *prometheus.CounterVec
	lastSuccessAt prometheus.Gauge
}

func NewSamplerMetrics(registerer prometheus.Registerer) *SamplerMetrics {
	m := &SamplerMetrics{
		observed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "replicasense_observed_demand",
			Help: "Latest source demand observed and persisted by ReplicaSense.",
		}, []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"}),
		cycles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "replicasense_sampler_cycles_total",
			Help: "Sampler cycles completed by outcome.",
		}, []string{"outcome"}),
		lastSuccessAt: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "replicasense_sampler_last_success_unixtime",
			Help: "Unix timestamp of the most recent fully successful sampler cycle.",
		}),
	}
	registerer.MustRegister(m.observed, m.cycles, m.lastSuccessAt)
	return m
}

func (m *SamplerMetrics) RecordCycle(now time.Time, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.cycles.WithLabelValues("error").Inc()
		return
	}
	m.cycles.WithLabelValues("success").Inc()
	m.lastSuccessAt.Set(float64(now.Unix()))
}

func (m *SamplerMetrics) RecordSample(workload workloadstore.SamplingWorkload, value float64) {
	if m == nil {
		return
	}
	key := workload.Spec.Key
	m.observed.WithLabelValues(key.ClusterID, key.Namespace, key.ScaledObjectName, key.PredictiveTriggerName).Set(value)
}
