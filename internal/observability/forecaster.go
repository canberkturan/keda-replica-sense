// Package observability contains low-cardinality ReplicaSense Prometheus metrics.
package observability

import (
	"strconv"
	"strings"
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
	sourceAge  *prometheus.GaugeVec
	model      *prometheus.GaugeVec
	capacity   *prometheus.GaugeVec
	cycles     *prometheus.CounterVec
	lastCycle  prometheus.Gauge
	estimated  *prometheus.GaugeVec
	ready      *prometheus.GaugeVec
	status     *prometheus.GaugeVec
}

func NewForecasterMetrics(registerer prometheus.Registerer) *ForecasterMetrics {
	labels := []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"}
	m := &ForecasterMetrics{
		forecast:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_demand", Help: "ReplicaSense forecast horizon-maximum demand."}, append(labels, "quantile")),
		predictive: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_predictive_metric", Help: "Safety-approved predictive replica demand."}, labels),
		surge:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_surge_projection_demand", Help: "Deterministic anomaly projection in source demand units; zero means inactive."}, labels),
		age:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_snapshot_age_seconds", Help: "Age of the newest forecaster snapshot."}, labels),
		sourceAge:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_source_age_seconds", Help: "Age of the source observation used by the newest forecast snapshot."}, labels),
		model:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_info", Help: "Active forecast model information."}, append(labels, "engine")),
		capacity:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_cluster_resource_capacity", Help: "Cluster resource accounting used for the speculative predictive budget. CPU is cores; memory is bytes."}, []string{"cluster_id", "resource", "state"}),
		cycles:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "replicasense_forecaster_cycles_total", Help: "Forecaster cycles completed by outcome."}, []string{"outcome"}),
		lastCycle:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "replicasense_forecaster_last_success_unixtime", Help: "Unix timestamp of the most recent fully successful forecaster cycle."}),
		estimated:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_input_estimated_samples", Help: "Number of inference-only interpolated inputs in the latest successful forecast; these are not observed or training samples."}, labels),
		ready:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_ready", Help: "One when the latest workload forecast succeeded, zero when it is withheld."}, labels),
		status:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_forecast_status", Help: "Latest workload forecast state, with a bounded reason label; value is one."}, append(labels, "reason")),
	}
	registerer.MustRegister(m.forecast, m.predictive, m.surge, m.age, m.sourceAge, m.model, m.capacity, m.cycles, m.lastCycle, m.estimated, m.ready, m.status)
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
	m.sourceAge.WithLabelValues(labels...).Set(now.Sub(snapshot.ObservedAt).Seconds())
	m.model.WithLabelValues(append(labels, snapshot.ModelEngine)...).Set(1)
	m.ready.WithLabelValues(labels...).Set(1)
	reason := "ready"
	if strings.Contains(snapshot.SafetyReason, ";inference_interpolated_points=") {
		reason = "interpolated_inputs"
	}
	m.recordStatus(workload, reason)
}

func (m *ForecasterMetrics) RecordInputQuality(workload workloadstore.SamplingWorkload, count int) {
	m.estimated.WithLabelValues(workload.Spec.Key.ClusterID, workload.Spec.Key.Namespace, workload.Spec.Key.ScaledObjectName, workload.Spec.Key.PredictiveTriggerName).Set(float64(count))
}

// RecordUnavailable distinguishes withholding from a zero-demand prediction
// and removes old gauges instead of showing a frozen previous guess.
func (m *ForecasterMetrics) RecordUnavailable(workload workloadstore.SamplingWorkload, reason string) {
	labels := forecastLabels(workload)
	for _, metric := range []*prometheus.GaugeVec{m.forecast, m.predictive, m.surge, m.age, m.sourceAge} {
		metric.DeletePartialMatch(labels)
	}
	m.estimated.With(labels).Set(0)
	m.ready.With(labels).Set(0)
	if reason == "model_unavailable" {
		m.model.DeletePartialMatch(labels)
	}
	m.recordStatus(workload, reason)
}

func (m *ForecasterMetrics) recordStatus(workload workloadstore.SamplingWorkload, reason string) {
	labels := forecastLabels(workload)
	m.status.DeletePartialMatch(labels)
	labels["reason"] = reason
	m.status.With(labels).Set(1)
}

func forecastLabels(workload workloadstore.SamplingWorkload) prometheus.Labels {
	key := workload.Spec.Key
	return prometheus.Labels{"cluster_id": key.ClusterID, "namespace": key.Namespace, "scaled_object": key.ScaledObjectName, "predictive_trigger": key.PredictiveTriggerName}
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
