package postgres

import (
	"context"
	"fmt"

	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TrainingRepository struct{ database *pgxpool.Pool }

func NewTrainingRepository(pool *pgxpool.Pool) *TrainingRepository {
	return &TrainingRepository{database: pool}
}

func (r *TrainingRepository) Claim(ctx context.Context, claim training.Claim) (*training.Run, bool, error) {
	var run training.Run
	query := `INSERT INTO training_runs (cluster_id,workload_id,training_slot,status,engine,training_window_start,training_window_end)
		VALUES ($1,$2,$3,'claimed',$4,$5,$6)
		ON CONFLICT (cluster_id,workload_id,training_slot) DO NOTHING
		RETURNING id`
	args := []any{claim.ClusterID, claim.WorkloadID, claim.Slot, claim.Engine, claim.TrainingWindowFrom, claim.TrainingWindowTo}
	if !claim.ExclusiveSince.IsZero() {
		query = `INSERT INTO training_runs (cluster_id,workload_id,training_slot,status,engine,training_window_start,training_window_end)
			SELECT $1,$2,$3,'claimed',$4,$5,$6
			WHERE NOT EXISTS (SELECT 1 FROM training_runs WHERE cluster_id=$1 AND workload_id=$2 AND created_at >= $7)
			ON CONFLICT (cluster_id,workload_id,training_slot) DO NOTHING
			RETURNING id`
		args = append(args, claim.ExclusiveSince)
	}
	err := r.database.QueryRow(ctx, query, args...).Scan(&run.ID)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim training run: %w", err)
	}
	return &run, true, nil
}

// ClaimImmediate reserves a deterministic policy-revision slot. If a
// controller/scheduler process died after reserving the run but before it
// created the Kubernetes Job, a still-claimed run is returned so the
// idempotent Job creator can repair that gap on the next loop.
func (r *TrainingRepository) ClaimImmediate(ctx context.Context, claim training.Claim) (*training.Run, bool, error) {
	var run training.Run
	err := r.database.QueryRow(ctx, `INSERT INTO training_runs (cluster_id,workload_id,training_slot,status,engine,training_window_start,training_window_end)
		VALUES ($1,$2,$3,'claimed',$4,$5,$6)
		ON CONFLICT (cluster_id,workload_id,training_slot) DO NOTHING
		RETURNING id`, claim.ClusterID, claim.WorkloadID, claim.Slot, claim.Engine, claim.TrainingWindowFrom, claim.TrainingWindowTo).Scan(&run.ID)
	if err == nil {
		return &run, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, fmt.Errorf("claim immediate training run: %w", err)
	}
	err = r.database.QueryRow(ctx, `SELECT id FROM training_runs WHERE cluster_id=$1 AND workload_id=$2 AND training_slot=$3 AND status='claimed'`, claim.ClusterID, claim.WorkloadID, claim.Slot).Scan(&run.ID)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load pending immediate training run: %w", err)
	}
	return &run, true, nil
}

func (r *TrainingRepository) MarkFailed(ctx context.Context, runID, reason string) error {
	_, err := r.database.Exec(ctx, `UPDATE training_runs SET status='failed',failure_reason=$2,finished_at=now() WHERE id=$1`, runID, reason)
	if err != nil {
		return fmt.Errorf("mark training run failed: %w", err)
	}
	return nil
}

func (r *TrainingRepository) MarkRunning(ctx context.Context, runID string) error {
	_, err := r.database.Exec(ctx, `UPDATE training_runs SET status='running',started_at=now(),failure_reason=NULL WHERE id=$1 AND status='claimed'`, runID)
	if err != nil {
		return fmt.Errorf("mark training run running: %w", err)
	}
	return nil
}

func (r *TrainingRepository) MarkSucceeded(ctx context.Context, runID, modelID string, datasetSize int, validationMetrics []byte) error {
	_, err := r.database.Exec(ctx, `UPDATE training_runs SET status='succeeded',model_id=$2,dataset_size=$3,validation_metrics=$4::jsonb,finished_at=now() WHERE id=$1 AND status='running'`, runID, modelID, datasetSize, validationMetrics)
	if err != nil {
		return fmt.Errorf("mark training run succeeded: %w", err)
	}
	return nil
}
