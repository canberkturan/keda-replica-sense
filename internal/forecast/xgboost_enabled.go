//go:build xgboost

package forecast

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/features"
	native "github.com/canberkturan/keda-replica-sense/internal/xgboost"
)

const xgboostFeatureSchema = features.SchemaV2

type xgboostArtifact struct {
	Engine        string `json:"engine"`
	Format        string `json:"format"`
	FeatureSchema string `json:"feature_schema"`
	P50Artifact   string `json:"p50_artifact_base64"`
	P95Artifact   string `json:"p95_artifact_base64"`
}

type xgboostModel struct {
	p50 *native.Model
	p95 *native.Model
}

func (m *xgboostModel) Engine() string { return "xgboost" }

func (m *xgboostModel) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	if m == nil || m.p50 == nil || m.p95 == nil {
		return Prediction{}, fmt.Errorf("xgboost model is unavailable")
	}
	featureSet, err := newXGBoostFeatureSet(samples, request)
	if err != nil {
		return Prediction{}, err
	}
	row, err := featureSet.row(len(samples) - 1)
	if err != nil {
		return Prediction{}, err
	}
	p50Predictions, err := m.p50.Predict(row, 1, len(row))
	if err != nil {
		return Prediction{}, err
	}
	p95Predictions, err := m.p95.Predict(row, 1, len(row))
	if err != nil {
		return Prediction{}, err
	}
	p50 := math.Max(0, float64(p50Predictions[0]))
	return Prediction{P50: p50, P95: math.Max(p50, float64(p95Predictions[0]))}, nil
}

// TrainXGBoost creates immutable P50 and P95 UBJSON boosters. Both models
// learn the maximum observed demand in the requested future horizon, which is
// the exact quantity used by predictive scaling. The P95 is trained with the
// native quantile objective rather than adding an optimistic in-sample
// residual, which avoids leaking training fit into uncertainty estimates.
func TrainXGBoost(samples []domain.Sample, request Request) (Model, []byte, error) {
	features, labels, err := xgboostDataset(samples, request)
	if err != nil {
		return nil, nil, err
	}
	columns := len(xgboostFeatureNames(samples, request))
	p50, err := native.Train(features, labels, len(labels), columns, native.TrainOptions{Rounds: 80, MaxDepth: 4, Eta: 0.1})
	if err != nil {
		return nil, nil, err
	}
	p95, err := native.Train(features, labels, len(labels), columns, native.TrainOptions{Rounds: 120, MaxDepth: 4, Eta: 0.08, Objective: "reg:quantileerror", QuantileAlpha: .95})
	if err != nil {
		p50.Close()
		return nil, nil, err
	}
	p50Artifact, err := p50.Artifact()
	if err != nil {
		p50.Close()
		p95.Close()
		return nil, nil, err
	}
	p95Artifact, err := p95.Artifact()
	if err != nil {
		p50.Close()
		p95.Close()
		return nil, nil, err
	}
	artifact, err := json.Marshal(xgboostArtifact{Engine: "xgboost", Format: "replicasense-xgboost-v2", FeatureSchema: xgboostFeatureSchema, P50Artifact: base64.StdEncoding.EncodeToString(p50Artifact), P95Artifact: base64.StdEncoding.EncodeToString(p95Artifact)})
	if err != nil {
		p50.Close()
		p95.Close()
		return nil, nil, err
	}
	return &xgboostModel{p50: p50, p95: p95}, artifact, nil
}

// ValidateXGBoost uses expanding windows. Each evaluation model is trained
// only on samples before the cut, preventing future-data leakage.
func ValidateXGBoost(samples []domain.Sample, request Request, maxWindows int) (ValidationMetrics, error) {
	horizonSteps, seasonSteps := int(request.Horizon/request.SamplingInterval), int(request.SeasonalPeriod/request.SamplingInterval)
	if horizonSteps < 1 || seasonSteps < 1 || len(samples) < seasonSteps+horizonSteps+2 {
		return ValidationMetrics{}, fmt.Errorf("insufficient samples for xgboost walk-forward validation")
	}
	start := seasonSteps + horizonSteps
	if maxWindows > 0 && len(samples)-horizonSteps-start > maxWindows {
		start = len(samples) - horizonSteps - maxWindows
	}
	var metrics ValidationMetrics
	for cut := start; cut+horizonSteps <= len(samples); cut++ {
		model, _, err := TrainXGBoost(samples[:cut], request)
		if err != nil {
			return ValidationMetrics{}, err
		}
		prediction, err := model.PredictSamples(samples[:cut], request)
		model.(*xgboostModel).Close()
		if err != nil {
			return ValidationMetrics{}, err
		}
		actual := horizonMaximum(samples, cut, horizonSteps)
		metrics.Windows++
		if prediction.P95 >= actual {
			metrics.Coverage++
		} else {
			metrics.UnderpredictionRate++
		}
		metrics.MeanAbsoluteError += math.Abs(prediction.P95 - actual)
		if actual >= prediction.P95 {
			metrics.PinballLossP95 += .95 * (actual - prediction.P95)
		} else {
			metrics.PinballLossP95 += .05 * (prediction.P95 - actual)
		}
	}
	if metrics.Windows == 0 {
		return ValidationMetrics{}, fmt.Errorf("no xgboost validation windows")
	}
	count := float64(metrics.Windows)
	metrics.Coverage, metrics.UnderpredictionRate, metrics.MeanAbsoluteError, metrics.PinballLossP95 = metrics.Coverage/count, metrics.UnderpredictionRate/count, metrics.MeanAbsoluteError/count, metrics.PinballLossP95/count
	return metrics, nil
}

func loadXGBoostArtifact(id string, artifactBytes []byte) (Model, error) {
	var artifact xgboostArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		return nil, fmt.Errorf("decode xgboost artifact %s: %w", id, err)
	}
	if artifact.Format != "replicasense-xgboost-v2" || artifact.FeatureSchema != xgboostFeatureSchema || artifact.P50Artifact == "" || artifact.P95Artifact == "" {
		return nil, fmt.Errorf("invalid xgboost artifact %s", id)
	}
	p50Artifact, err := base64.StdEncoding.DecodeString(artifact.P50Artifact)
	if err != nil {
		return nil, fmt.Errorf("decode XGBoost P50 artifact %s: %w", id, err)
	}
	p95Artifact, err := base64.StdEncoding.DecodeString(artifact.P95Artifact)
	if err != nil {
		return nil, fmt.Errorf("decode XGBoost P95 artifact %s: %w", id, err)
	}
	p50, err := native.LoadArtifact(p50Artifact)
	if err != nil {
		return nil, fmt.Errorf("load XGBoost P50 artifact %s: %w", id, err)
	}
	p95, err := native.LoadArtifact(p95Artifact)
	if err != nil {
		p50.Close()
		return nil, fmt.Errorf("load XGBoost P95 artifact %s: %w", id, err)
	}
	return &xgboostModel{p50: p50, p95: p95}, nil
}

func (m *xgboostModel) Close() {
	if m == nil {
		return
	}
	if m.p50 != nil {
		m.p50.Close()
	}
	if m.p95 != nil {
		m.p95.Close()
	}
}

func xgboostDataset(samples []domain.Sample, request Request) ([]float32, []float32, error) {
	horizonSteps, seasonSteps := int(request.Horizon/request.SamplingInterval), int(request.SeasonalPeriod/request.SamplingInterval)
	if horizonSteps < 1 || seasonSteps < 1 || len(samples) <= seasonSteps+horizonSteps {
		return nil, nil, fmt.Errorf("insufficient samples for xgboost training")
	}
	featureSet, err := newXGBoostFeatureSet(samples, request)
	if err != nil {
		return nil, nil, err
	}
	columns := len(featureSet.names)
	features, labels := make([]float32, 0, (len(samples)-seasonSteps-horizonSteps)*columns), make([]float32, 0, len(samples)-seasonSteps-horizonSteps)
	start := seasonSteps
	if start < 60 {
		start = 60
	}
	for index := start; index+horizonSteps < len(samples); index++ {
		row, err := featureSet.row(index)
		if err != nil {
			return nil, nil, err
		}
		features = append(features, row...)
		labels = append(labels, float32(horizonMaximum(samples, index+1, horizonSteps)))
	}
	return features, labels, nil
}

func xgboostFeatures(samples []domain.Sample, index int, request Request) ([]float32, error) {
	featureSet, err := newXGBoostFeatureSet(samples, request)
	if err != nil {
		return nil, err
	}
	return featureSet.row(index)
}

type xgboostFeatureSet struct {
	points   []features.Point
	pipeline features.Pipeline
	names    []string
}

func newXGBoostFeatureSet(samples []domain.Sample, request Request) (xgboostFeatureSet, error) {
	if len(samples) <= 60 {
		return xgboostFeatureSet{}, fmt.Errorf("insufficient samples for xgboost features")
	}
	points := make([]features.Point, len(samples))
	for i := range samples {
		points[i] = features.Point{ObservedAt: samples[i].ObservedAt, Value: samples[i].ObservedValue}
	}
	pipeline := features.DefaultV2(request.BusinessLocation)
	vector, err := pipeline.Build(points, len(points)-1)
	if err != nil {
		return xgboostFeatureSet{}, err
	}
	names := make([]string, 0, len(vector))
	for name := range vector {
		names = append(names, name)
	}
	sort.Strings(names)
	return xgboostFeatureSet{points: points, pipeline: pipeline, names: names}, nil
}

func (s xgboostFeatureSet) row(index int) ([]float32, error) {
	if index < 60 || index >= len(s.points) {
		return nil, fmt.Errorf("insufficient samples for xgboost features")
	}
	vector, err := s.pipeline.Build(s.points, index)
	if err != nil {
		return nil, err
	}
	row := make([]float32, len(s.names))
	for i, name := range s.names {
		if !finite(vector[name]) {
			return nil, fmt.Errorf("xgboost feature %s is non-finite", name)
		}
		row[i] = float32(vector[name])
	}
	return row, nil
}

func xgboostFeatureNames(samples []domain.Sample, request Request) []string {
	featureSet, err := newXGBoostFeatureSet(samples, request)
	if err != nil {
		return nil
	}
	return featureSet.names
}

func horizonMaximum(samples []domain.Sample, from, steps int) float64 {
	maximum := samples[from].ObservedValue
	for _, sample := range samples[from+1 : from+steps] {
		if sample.ObservedValue > maximum {
			maximum = sample.ObservedValue
		}
	}
	return maximum
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

var _ = time.Second
