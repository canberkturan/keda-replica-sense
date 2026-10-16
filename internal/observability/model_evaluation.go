package observability

import (
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/prometheus/client_golang/prometheus"
)

type ModelEvaluationMetrics struct {
	decisions *prometheus.CounterVec
	state     *prometheus.GaugeVec
	scores    *prometheus.GaugeVec
	last      *prometheus.GaugeVec
}

func NewModelEvaluationMetrics(r prometheus.Registerer) *ModelEvaluationMetrics {
	labels := []string{"cluster_id", "namespace", "scaled_object", "predictive_trigger"}
	m := &ModelEvaluationMetrics{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "replicasense_model_evaluation_decisions_total", Help: "Completed shared-artifact evaluation decisions by bounded action."}, append(labels, "action")),
		state:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_evaluation_status", Help: "Latest model evaluation decision, including shadow and withholding states."}, append(labels, "action", "reason")),
		scores:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_evaluation_score", Help: "Paired real-observation artifact scores; excess replica minutes are a demand-only proxy."}, append(labels, "role", "score")),
		last:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "replicasense_model_evaluation_last_unixtime", Help: "Time of the latest completed model evaluation attempt, not necessarily successful promotion."}, labels),
	}
	r.MustRegister(m.decisions, m.state, m.scores, m.last)
	return m
}

func (m *ModelEvaluationMetrics) Record(w workloadstore.SamplingWorkload, report training.EvaluationReport) {
	labels := forecastLabels(w)
	values := []string{w.Spec.Key.ClusterID, w.Spec.Key.Namespace, w.Spec.Key.ScaledObjectName, w.Spec.Key.PredictiveTriggerName}
	m.decisions.WithLabelValues(append(values, report.Action)...).Inc()
	m.state.DeletePartialMatch(labels)
	m.scores.DeletePartialMatch(labels)
	m.state.WithLabelValues(append(values, report.Action, report.Reason)...).Set(1)
	m.last.WithLabelValues(values...).Set(float64(report.At.Unix()))
	for role, score := range report.Evidence.Metrics {
		for name, value := range map[string]float64{"coverage": score.Coverage, "underprediction_rate": score.UnderpredictionRate, "pinball_loss": score.PinballLossP95, "mean_underprediction": score.MeanUnderprediction, "peak_mean_underprediction": score.PeakMeanUnderprediction, "max_relative_underprediction": score.MaxRelativeUnderprediction, "excess_replica_minutes": score.ExcessReplicaMinutes, "windows": float64(score.Windows)} {
			m.scores.WithLabelValues(append(values, role, name)...).Set(value)
		}
	}
}
