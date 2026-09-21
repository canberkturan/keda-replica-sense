// Package postgres implements ReplicaSense persistence contracts using pgx.
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

type transactionStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type WorkloadRepository struct {
	database transactionStarter
}

func NewWorkloadRepository(pool *pgxpool.Pool) *WorkloadRepository {
	return &WorkloadRepository{database: pool}
}

func (r *WorkloadRepository) Reconcile(ctx context.Context, reconciliation workloadstore.Reconciliation) error {
	tx, err := r.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin workload reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	triggerNames := make([]string, 0, len(reconciliation.Desired))
	for _, workload := range reconciliation.Desired {
		if err := upsertWorkload(ctx, tx, reconciliation, workload); err != nil {
			return err
		}
		triggerNames = append(triggerNames, workload.Key.PredictiveTriggerName)
	}
	if _, err := tx.Exec(ctx, deactivateMissingWorkloadsSQL,
		reconciliation.Parent.ClusterID,
		reconciliation.Parent.Namespace,
		reconciliation.Parent.ScaledObjectName,
		triggerNames,
	); err != nil {
		return fmt.Errorf("deactivate missing workloads: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit workload reconciliation: %w", err)
	}
	return nil
}

func (r *WorkloadRepository) DeactivateParent(ctx context.Context, parent workloadstore.ParentKey) error {
	tx, err := r.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin parent deactivation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, deactivateParentSQL, parent.ClusterID, parent.Namespace, parent.ScaledObjectName); err != nil {
		return fmt.Errorf("deactivate parent workloads: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit parent deactivation: %w", err)
	}
	return nil
}

func upsertWorkload(ctx context.Context, tx pgx.Tx, reconciliation workloadstore.Reconciliation, workload domain.WorkloadSpec) error {
	var workloadID, previousPolicy string
	err := tx.QueryRow(ctx, `SELECT id, policy_revision FROM workloads WHERE cluster_id=$1 AND namespace=$2 AND scaled_object_name=$3 AND predictive_trigger_name=$4 FOR UPDATE`, workload.Key.ClusterID, workload.Key.Namespace, workload.Key.ScaledObjectName, workload.Key.PredictiveTriggerName).Scan(&workloadID, &previousPolicy)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("load existing workload for reconciliation: %w", err)
	}
	existed := err == nil
	if _, err := tx.Exec(ctx, upsertWorkloadSQL,
		workload.Key.ClusterID,
		workload.Key.Namespace,
		workload.Key.ScaledObjectName,
		workload.Key.PredictiveTriggerName,
		reconciliation.ScaledObjectUID,
		reconciliation.ObservedGeneration,
		workload.ScaleTarget.Name,
		workload.Bounds.Min,
		workload.Bounds.Max,
		workload.Source.TriggerName,
		workload.Source.ServerAddress,
		workload.Source.Query,
		workload.Source.Threshold,
		workload.SourceFingerprint,
		int64(workload.Forecast.Horizon/time.Second),
		int64(workload.Forecast.StartupLatency/time.Second),
		int64(workload.Forecast.SafetyBuffer/time.Second),
		workload.Forecast.Quantile,
		int64(workload.Forecast.TrainingWindow/time.Second),
		int64(workload.Forecast.SamplingInterval/time.Second),
		workload.Forecast.ModelEngine,
		workload.Forecast.BusinessTimezone,
		workload.Forecast.ImmediateTraining,
		workload.Forecast.ClearOldModels,
		workload.PolicyRevision,
	); err != nil {
		return fmt.Errorf("upsert workload %s/%s/%s/%s: %w", workload.Key.ClusterID, workload.Key.Namespace, workload.Key.ScaledObjectName, workload.Key.PredictiveTriggerName, err)
	}
	if workload.Forecast.ClearOldModels && (!existed || previousPolicy != workload.PolicyRevision) {
		if !existed {
			if err := tx.QueryRow(ctx, `SELECT id FROM workloads WHERE cluster_id=$1 AND namespace=$2 AND scaled_object_name=$3 AND predictive_trigger_name=$4`, workload.Key.ClusterID, workload.Key.Namespace, workload.Key.ScaledObjectName, workload.Key.PredictiveTriggerName).Scan(&workloadID); err != nil {
				return fmt.Errorf("load reconciled workload: %w", err)
			}
		}
		// Keep training-run audit rows while removing all model artifacts for the
		// stable workload ID, including previous source fingerprints.
		if _, err := tx.Exec(ctx, `UPDATE training_runs SET model_id=NULL WHERE workload_id=$1`, workloadID); err != nil {
			return fmt.Errorf("detach cleared models from training runs: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM models WHERE workload_id=$1`, workloadID); err != nil {
			return fmt.Errorf("clear workload models: %w", err)
		}
	}
	return nil
}

const upsertWorkloadSQL = `
INSERT INTO workloads (
    cluster_id, namespace, scaled_object_name, predictive_trigger_name,
    scaled_object_uid, observed_generation, scale_target_name,
    min_replica_count, max_replica_count, source_trigger_name,
    prometheus_server_address, prometheus_query, reactive_threshold,
    source_fingerprint, forecast_horizon_seconds, startup_latency_seconds, safety_buffer_seconds, forecast_quantile,
    training_window_seconds, sampling_interval_seconds, model_engine, business_timezone, immediate_training, clear_old_models,
    policy_revision, status, validation_errors, deactivated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, 'active', '[]'::jsonb, NULL
)
ON CONFLICT (cluster_id, namespace, scaled_object_name, predictive_trigger_name)
DO UPDATE SET
    scaled_object_uid = EXCLUDED.scaled_object_uid,
    observed_generation = EXCLUDED.observed_generation,
    scale_target_name = EXCLUDED.scale_target_name,
    min_replica_count = EXCLUDED.min_replica_count,
    max_replica_count = EXCLUDED.max_replica_count,
    source_trigger_name = EXCLUDED.source_trigger_name,
    prometheus_server_address = EXCLUDED.prometheus_server_address,
    prometheus_query = EXCLUDED.prometheus_query,
    reactive_threshold = EXCLUDED.reactive_threshold,
    source_fingerprint = EXCLUDED.source_fingerprint,
    forecast_horizon_seconds = EXCLUDED.forecast_horizon_seconds,
    startup_latency_seconds = EXCLUDED.startup_latency_seconds,
    safety_buffer_seconds = EXCLUDED.safety_buffer_seconds,
    forecast_quantile = EXCLUDED.forecast_quantile,
    training_window_seconds = EXCLUDED.training_window_seconds,
    sampling_interval_seconds = EXCLUDED.sampling_interval_seconds,
    model_engine = EXCLUDED.model_engine,
    business_timezone = EXCLUDED.business_timezone,
    immediate_training = EXCLUDED.immediate_training,
    clear_old_models = EXCLUDED.clear_old_models,
    policy_revision = EXCLUDED.policy_revision,
    status = 'active',
    validation_errors = '[]'::jsonb,
    deactivated_at = NULL`

const deactivateMissingWorkloadsSQL = `
UPDATE workloads
SET status = 'inactive', deactivated_at = COALESCE(deactivated_at, now())
WHERE cluster_id = $1
  AND namespace = $2
  AND scaled_object_name = $3
  AND status = 'active'
  AND NOT (predictive_trigger_name = ANY($4))`

const deactivateParentSQL = `
UPDATE workloads
SET status = 'inactive', deactivated_at = COALESCE(deactivated_at, now())
WHERE cluster_id = $1
  AND namespace = $2
  AND scaled_object_name = $3
  AND status = 'active'`
