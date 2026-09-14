// Package trainer executes a single claimed training run inside a Kubernetes Job.
package trainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/dataset"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/features"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type Workloads interface {
	ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error)
}
type Samples interface {
	ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error)
}
type Runs interface {
	MarkRunning(context.Context, string) error
	MarkSucceeded(context.Context, string, string, int, []byte) error
	MarkFailed(context.Context, string, string) error
}
type Models interface {
	CreateCandidate(context.Context, training.ModelCandidate) (string, error)
	Activate(context.Context, string) error
	ActiveValidation(context.Context, string, string, string) (*forecast.ValidationMetrics, error)
}

type Service struct {
	Workloads Workloads
	Samples   Samples
	Runs      Runs
	Models    Models
}

func (s Service) Run(ctx context.Context, clusterID, workloadID, runID, engine string, now time.Time) (err error) {
	if s.Workloads == nil || s.Samples == nil || s.Runs == nil || s.Models == nil {
		return fmt.Errorf("trainer requires workloads, samples, runs, and models")
	}
	if err := s.Runs.MarkRunning(ctx, runID); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = s.Runs.MarkFailed(ctx, runID, err.Error())
		}
	}()
	workloads, err := s.Workloads.ListActiveForCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	var workload *workloadstore.SamplingWorkload
	for index := range workloads {
		if workloads[index].ID == workloadID {
			workload = &workloads[index]
			break
		}
	}
	if workload == nil {
		return fmt.Errorf("active workload %s not found", workloadID)
	}
	if engine == "" {
		engine = workload.Spec.Forecast.ModelEngine
	}
	end := now.UTC().Truncate(workload.Spec.Forecast.SamplingInterval)
	start := end.Add(-workload.Spec.Forecast.TrainingWindow)
	samples, err := s.Samples.ListSamplesForWindow(ctx, *workload, start, end)
	if err != nil {
		return err
	}
	minimum := int((24 * time.Hour) / workload.Spec.Forecast.SamplingInterval)
	if err := dataset.Validate(samples, dataset.QualityPolicy{MinSamples: minimum, SamplingInterval: workload.Spec.Forecast.SamplingInterval, MaxGapIntervals: 2}); err != nil {
		return err
	}
	location, err := time.LoadLocation(workload.Spec.Forecast.BusinessTimezone)
	if err != nil {
		return fmt.Errorf("load workload business timezone: %w", err)
	}
	request := forecast.Request{Horizon: workload.Spec.Forecast.DecisionHorizon(), SamplingInterval: workload.Spec.Forecast.SamplingInterval, SeasonalPeriod: 24 * time.Hour, Quantile: workload.Spec.Forecast.Quantile, BusinessLocation: location}
	var model forecast.Model
	var validation forecast.ValidationMetrics
	var artifact []byte
	if engine == "xgboost" {
		model, artifact, err = forecast.TrainXGBoost(samples, request)
		if err == nil {
			validation, err = forecast.ValidateXGBoost(samples, request, 48)
		}
	} else {
		model, err = forecast.ResolveModel(engine)
		if err == nil {
			validation, err = forecast.WalkForward(model, samples, request, 288)
		}
		if err == nil {
			artifactMetadata := map[string]any{"format": "replicasense-model-v1", "engine": model.Engine()}
			if model.Engine() == "seasonal-baseline" {
				artifactMetadata["seasonalPeriod"] = "24h"
			}
			artifact, err = json.Marshal(artifactMetadata)
		}
	}
	if err != nil {
		return err
	}
	// The deterministic seasonal baseline is evaluated on the same expanding
	// windows as the candidate. It provides an auditable no-champion reference
	// without looking beyond each validation cut.
	baselineValidation, err := forecast.WalkForward(forecast.SeasonalBaseline{}, samples, request, 288)
	if err != nil {
		return fmt.Errorf("validate deterministic baseline: %w", err)
	}
	champion, championErr := s.Models.ActiveValidation(ctx, clusterID, workload.ID, workload.Spec.SourceFingerprint)
	decision := training.PromotionDecision{}
	if championErr != nil {
		decision = training.PromotionDecision{Reason: "active_champion_validation_unavailable"}
	} else {
		decision = training.EvaluatePromotion(training.DefaultPromotionPolicy(), validation, champion, baselineValidation)
	}
	metrics, err := json.Marshal(map[string]any{"sample_count": len(samples), "baseline": model.Engine() != "xgboost", "operational_quantile": forecastOperationalQuantile(request), "walk_forward": validation, "deterministic_baseline": baselineValidation, "promotion": decision})
	if err != nil {
		return err
	}
	datasetDigest := fingerprint(samples)
	artifactHash := sha256.Sum256(artifact)
	featureSchema := featureSchemaFor(model.Engine())
	modelID, err := s.Models.CreateCandidate(ctx, training.ModelCandidate{ClusterID: clusterID, WorkloadID: workload.ID, SourceFingerprint: workload.Spec.SourceFingerprint, Engine: model.Engine(), FeatureSchema: featureSchema, TrainingWindowStart: start, TrainingWindowEnd: end, DatasetFingerprint: datasetDigest, Hyperparameters: []byte(`{}`), ValidationMetrics: metrics, Artifact: artifact, ArtifactSHA256: hex.EncodeToString(artifactHash[:])})
	if err != nil {
		return err
	}
	if decision.Promote {
		if err := s.Models.Activate(ctx, modelID); err != nil {
			return fmt.Errorf("activate trained model: %w", err)
		}
	}
	return s.Runs.MarkSucceeded(ctx, runID, modelID, len(samples), metrics)
}

func featureSchemaFor(engine string) string {
	if engine == "xgboost" {
		return features.SchemaV3
	}
	return "v1"
}

func forecastOperationalQuantile(request forecast.Request) float64 {
	if request.Quantile > 0 && request.Quantile < 1 {
		return request.Quantile
	}
	return 0.95
}

func fingerprint(samples []domain.Sample) string {
	hash := sha256.New()
	for _, sample := range samples {
		_, _ = hash.Write([]byte(sample.ObservedAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatFloat(sample.ObservedValue, 'g', -1, 64) + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
