// Package observability contains low-cardinality ReplicaSense Prometheus metrics.
package observability

import (
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

type ForecasterMetrics struct {
	forecast   *prometheus.GaugeVec
	predictive *prometheus.GaugeVec
	surge      *prometheus.GaugeVec
	age        *prometheus.GaugeVec
	model      *prometheus.GaugeVec
}

func NewForecasterMetrics(registerer prometheus.Registerer) *ForecasterMetrics {
	labels := []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"}
	m := &ForecasterMetrics{
		forecast:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_demand", Help: "ReplicaSense forecast horizon-maximum demand."}, append(labels, "quantile")),
		predictive: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_predictive_metric", Help: "Safety-approved predictive replica demand."}, labels),
		surge:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_surge_projection_demand", Help: "Deterministic anomaly projection in source demand units; zero means inactive."}, labels),
		age:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_snapshot_age_seconds", Help: "Age of the newest forecaster snapshot."}, labels),
		model:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_info", Help: "Active forecast model information."}, append(labels, "engine")),
	}
	registerer.MustRegister(m.forecast, m.predictive, m.surge, m.age, m.model)
	return m
}

func (m *ForecasterMetrics) Record(workload workloadstore.SamplingWorkload, snapshot domain.ForecastSnapshot, now time.Time) {
	labels := []string{workload.Spec.Key.ClusterID, workload.Spec.Key.Namespace, workload.Spec.Key.ScaledObjectName, workload.Spec.Key.PredictiveTriggerName}
	m.forecast.WithLabelValues(append(labels, "0.50")...).Set(snapshot.ForecastP50)
	quantile := workload.Spec.Forecast.Quantile
	if quantile <= 0 || quantile >= 1 {
		quantile = .95
	}
	m.forecast.WithLabelValues(append(labels, strconv.FormatFloat(quantile, 'f', -1, 64))...).Set(snapshot.ForecastP95)
	m.predictive.WithLabelValues(labels...).Set(snapshot.SafeDemand)
	m.surge.WithLabelValues(labels...).Set(snapshot.SurgeDemand)
	m.age.WithLabelValues(labels...).Set(now.Sub(snapshot.GeneratedAt).Seconds())
	m.model.WithLabelValues(append(labels, snapshot.ModelEngine)...).Set(1)
}
