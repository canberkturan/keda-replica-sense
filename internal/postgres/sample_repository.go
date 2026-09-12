package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

// SampleRepository is the PostgreSQL implementation used exclusively by the
// sampler. It deliberately exposes neither models nor forecasts.
type SampleRepository struct {
	database *pgxpool.Pool
}

func NewSampleRepository(pool *pgxpool.Pool) *SampleRepository {
	return &SampleRepository{database: pool}
}

func (r *SampleRepository) ListActiveForCluster(ctx context.Context, clusterID string) ([]workloadstore.SamplingWorkload, error) {
	rows, err := r.database.Query(ctx, listActiveSamplingWorkloadsSQL, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list active sampling workloads: %w", err)
	}
	defer rows.Close()

	workloads := make([]workloadstore.SamplingWorkload, 0)
	for rows.Next() {
		var workload workloadstore.SamplingWorkload
		var horizonSeconds, startupLatencySeconds, safetyBufferSeconds, trainingWindowSeconds, samplingIntervalSeconds int64
		if err := rows.Scan(
			&workload.ID,
			&workload.Spec.Key.ClusterID,
			&workload.Spec.Key.Namespace,
			&workload.Spec.Key.ScaledObjectName,
			&workload.Spec.Key.PredictiveTriggerName,
			&workload.Spec.ScaleTarget.Name,
			&workload.Spec.Bounds.Min,
			&workload.Spec.Bounds.Max,
			&workload.Spec.Source.TriggerName,
			&workload.Spec.Source.ServerAddress,
			&workload.Spec.Source.Query,
			&workload.Spec.Source.Threshold,
			&workload.Spec.SourceFingerprint,
			&horizonSeconds,
			&startupLatencySeconds,
			&safetyBufferSeconds,
			&workload.Spec.Forecast.Quantile,
			&trainingWindowSeconds,
			&samplingIntervalSeconds,
			&workload.Spec.Forecast.ModelEngine,
			&workload.Spec.Forecast.BusinessTimezone,
			&workload.Spec.PolicyRevision,
		); err != nil {
			return nil, fmt.Errorf("scan active sampling workload: %w", err)
		}
		workload.Spec.Forecast.Horizon = time.Duration(horizonSeconds) * time.Second
		workload.Spec.Forecast.StartupLatency = time.Duration(startupLatencySeconds) * time.Second
		workload.Spec.Forecast.SafetyBuffer = time.Duration(safetyBufferSeconds) * time.Second
		workload.Spec.Forecast.TrainingWindow = time.Duration(trainingWindowSeconds) * time.Second
		workload.Spec.Forecast.SamplingInterval = time.Duration(samplingIntervalSeconds) * time.Second
		workloads = append(workloads, workload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active sampling workloads: %w", err)
	}
	return workloads, nil
}

func (r *SampleRepository) UpsertSamples(ctx context.Context, samples []domain.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, sample := range samples {
		batch.Queue(upsertSampleSQL, sample.ClusterID, sample.WorkloadID, sample.SourceFingerprint, sample.ObservedAt, sample.ObservedValue)
	}
	results := r.database.SendBatch(ctx, batch)
	defer results.Close()
	for range samples {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert sample: %w", err)
		}
	}
	return nil
}

func (r *SampleRepository) GetSampleCoverage(ctx context.Context, workload workloadstore.SamplingWorkload, start, end time.Time) (workloadstore.SampleCoverage, error) {
	var coverage workloadstore.SampleCoverage
	err := r.database.QueryRow(ctx, sampleCoverageSQL, workload.Spec.Key.ClusterID, workload.ID, workload.Spec.SourceFingerprint, start, end).Scan(&coverage.OldestObservedAt, &coverage.NewestObservedAt, &coverage.Count)
	if err != nil {
		return workloadstore.SampleCoverage{}, fmt.Errorf("get sample coverage: %w", err)
	}
	return coverage, nil
}

func (r *SampleRepository) ListSamplesForWindow(ctx context.Context, workload workloadstore.SamplingWorkload, start, end time.Time) ([]domain.Sample, error) {
	rows, err := r.database.Query(ctx, listSamplesForWindowSQL, workload.Spec.Key.ClusterID, workload.ID, workload.Spec.SourceFingerprint, start, end)
	if err != nil {
		return nil, fmt.Errorf("list training samples: %w", err)
	}
	defer rows.Close()
	var samples []domain.Sample
	for rows.Next() {
		var sample domain.Sample
		if err := rows.Scan(&sample.ClusterID, &sample.WorkloadID, &sample.SourceFingerprint, &sample.ObservedAt, &sample.ObservedValue); err != nil {
			return nil, fmt.Errorf("scan training sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate training samples: %w", err)
	}
	return samples, nil
}

const listActiveSamplingWorkloadsSQL = `
SELECT
    id, cluster_id, namespace, scaled_object_name, predictive_trigger_name,
    scale_target_name, min_replica_count, max_replica_count, source_trigger_name,
    prometheus_server_address, prometheus_query, reactive_threshold,
    source_fingerprint, forecast_horizon_seconds, startup_latency_seconds, safety_buffer_seconds, forecast_quantile,
    training_window_seconds, sampling_interval_seconds, model_engine, business_timezone, policy_revision
FROM workloads
WHERE cluster_id = $1 AND status = 'active'
ORDER BY namespace, scaled_object_name, predictive_trigger_name`

const upsertSampleSQL = `
INSERT INTO samples (cluster_id, workload_id, source_fingerprint, observed_at, observed_value)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (cluster_id, workload_id, source_fingerprint, observed_at)
DO UPDATE SET observed_value = EXCLUDED.observed_value`

const sampleCoverageSQL = `
SELECT COALESCE(min(observed_at), 'epoch'::timestamptz),
       COALESCE(max(observed_at), 'epoch'::timestamptz),
       count(*)
FROM samples
WHERE cluster_id = $1
  AND workload_id = $2
  AND source_fingerprint = $3
  AND observed_at >= $4
  AND observed_at <= $5`

const listSamplesForWindowSQL = `
SELECT cluster_id, workload_id, source_fingerprint, observed_at, observed_value
FROM samples
WHERE cluster_id = $1 AND workload_id = $2 AND source_fingerprint = $3
  AND observed_at >= $4 AND observed_at <= $5
ORDER BY observed_at ASC`
