// Package observability contains low-cardinality ReplicaSense Prometheus metrics.
package observability

import (
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/kube"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

type ForecasterMetrics struct {
	forecast   *prometheus.GaugeVec
	predictive *prometheus.GaugeVec
	surge      *prometheus.GaugeVec
	age        *prometheus.GaugeVec
	model      *prometheus.GaugeVec
	capacity   *prometheus.GaugeVec
	cycles     *prometheus.CounterVec
	lastCycle  prometheus.Gauge
}

func NewForecasterMetrics(registerer prometheus.Registerer) *ForecasterMetrics {
	labels := []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"}
	m := &ForecasterMetrics{
		forecast:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_demand", Help: "ReplicaSense forecast horizon-maximum demand."}, append(labels, "quantile")),
		predictive: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_predictive_metric", Help: "Safety-approved predictive replica demand."}, labels),
		surge:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_surge_projection_demand", Help: "Deterministic anomaly projection in source demand units; zero means inactive."}, labels),
		age:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_snapshot_age_seconds", Help: "Age of the newest forecaster snapshot."}, labels),
		model:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_info", Help: "Active forecast model information."}, append(labels, "engine")),
		capacity:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_cluster_resource_capacity", Help: "Cluster resource accounting used for the speculative predictive budget. CPU is cores; memory is bytes."}, []string{"cluster_id", "resource", "state"}),
		cycles:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "replicasense_forecaster_cycles_total", Help: "Forecaster cycles completed by outcome."}, []string{"outcome"}),
		lastCycle:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "replicasense_forecaster_last_success_unixtime", Help: "Unix timestamp of the most recent fully successful forecaster cycle."}),
	}
	registerer.MustRegister(m.forecast, m.predictive, m.surge, m.age, m.model, m.capacity, m.cycles, m.lastCycle)
	return m
}

func (m *ForecasterMetrics) RecordCycle(now time.Time, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.cycles.WithLabelValues("error").Inc()
		return
	}
	m.cycles.WithLabelValues("success").Inc()
	m.lastCycle.Set(float64(now.Unix()))
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

// RecordCapacity exports the cluster-wide inputs to the capacity guard. The
// metric has no workload or query labels, so it remains bounded even in a
// multi-tenant cluster.
func (m *ForecasterMetrics) RecordCapacity(clusterID string, snapshot kube.CapacitySnapshot) {
	if m == nil {
		return
	}
	m.capacity.WithLabelValues(clusterID, "cpu", "allocatable").Set(snapshot.AllocatableCPU())
	m.capacity.WithLabelValues(clusterID, "cpu", "requested").Set(snapshot.RequestedCPU())
	m.capacity.WithLabelValues(clusterID, "cpu", "available").Set(snapshot.AvailableCPU())
	m.capacity.WithLabelValues(clusterID, "cpu", "speculative_budget").Set(snapshot.BudgetCPU())
	m.capacity.WithLabelValues(clusterID, "memory", "allocatable").Set(snapshot.AllocatableMemory())
	m.capacity.WithLabelValues(clusterID, "memory", "requested").Set(snapshot.RequestedMemory())
	m.capacity.WithLabelValues(clusterID, "memory", "available").Set(snapshot.AvailableMemory())
	m.capacity.WithLabelValues(clusterID, "memory", "speculative_budget").Set(snapshot.BudgetMemory())
}
