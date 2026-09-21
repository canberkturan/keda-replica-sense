package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ScalerMetrics describes the externally visible health of the KEDA gRPC
// boundary. Labels are intentionally limited to the three protocol methods
// and a served/withheld outcome.
type ScalerMetrics struct {
	requests    *prometheus.CounterVec
	snapshotAge prometheus.Gauge
}

func NewScalerMetrics(registerer prometheus.Registerer) *ScalerMetrics {
	m := &ScalerMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "replicasense_scaler_requests_total",
			Help: "KEDA external-scaler requests handled by ReplicaSense.",
		}, []string{"method", "outcome"}),
		snapshotAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "replicasense_scaler_served_snapshot_age_seconds",
			Help: "Age of the most recent predictive snapshot served by the external scaler; zero when the latest request was withheld.",
		}),
	}
	registerer.MustRegister(m.requests, m.snapshotAge)
	return m
}

func (m *ScalerMetrics) RecordRequest(method string, served bool, age time.Duration) {
	if m == nil {
		return
	}
	outcome := "withheld"
	if served {
		outcome = "served"
	}
	m.requests.WithLabelValues(method, outcome).Inc()
	if served && age >= 0 {
		m.snapshotAge.Set(age.Seconds())
		return
	}
	m.snapshotAge.Set(0)
}
