package postgres

import (
	"context"
	"fmt"

	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelRepository owns immutable candidate storage and atomic activation.
type ModelRepository struct{ database *pgxpool.Pool }

func NewModelRepository(pool *pgxpool.Pool) *ModelRepository { return &ModelRepository{database: pool} }

func (r *ModelRepository) CreateCandidate(ctx context.Context, candidate training.ModelCandidate) (string, error) {
	var id string
	err := r.database.QueryRow(ctx, `INSERT INTO models
		(cluster_id,workload_id,source_fingerprint,engine,feature_schema_version,training_window_start,training_window_end,dataset_fingerprint,hyperparameters,validation_metrics,artifact_data,artifact_sha256,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11,$12,'candidate') RETURNING id`,
		candidate.ClusterID, candidate.WorkloadID, candidate.SourceFingerprint, candidate.Engine, candidate.FeatureSchema, candidate.TrainingWindowStart, candidate.TrainingWindowEnd, candidate.DatasetFingerprint, candidate.Hyperparameters, candidate.ValidationMetrics, candidate.Artifact, candidate.ArtifactSHA256).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create model candidate: %w", err)
	}
	return id, nil
}

func (r *ModelRepository) ListActiveModels(ctx context.Context, clusterID string) ([]forecast.StoredModel, error) {
	rows, err := r.database.Query(ctx, `SELECT id,workload_id,source_fingerprint,engine,artifact_data,activated_at FROM models WHERE cluster_id=$1 AND status='active' ORDER BY activated_at DESC`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list active models: %w", err)
	}
	defer rows.Close()
	var models []forecast.StoredModel
	for rows.Next() {
		var model forecast.StoredModel
		if err := rows.Scan(&model.ID, &model.WorkloadID, &model.SourceFingerprint, &model.Engine, &model.Artifact, &model.ActivatedAt); err != nil {
			return nil, fmt.Errorf("scan active model: %w", err)
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

// Activate atomically retires the previous active candidate for the same
// workload/source and promotes the requested candidate.
func (r *ModelRepository) Activate(ctx context.Context, id string) error {
	tx, err := r.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin model activation: %w", err)
	}
	defer tx.Rollback(ctx)
	var clusterID, workloadID, fingerprint string
	if err := tx.QueryRow(ctx, `SELECT cluster_id,workload_id,source_fingerprint FROM models WHERE id=$1 AND status='candidate' FOR UPDATE`, id).Scan(&clusterID, &workloadID, &fingerprint); err != nil {
		return fmt.Errorf("load candidate model: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE models SET status='retired',retired_at=now() WHERE cluster_id=$1 AND workload_id=$2 AND source_fingerprint=$3 AND status='active'`, clusterID, workloadID, fingerprint); err != nil {
		return fmt.Errorf("retire active model: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE models SET status='active',activated_at=now() WHERE id=$1 AND status='candidate'`, id); err != nil {
		return fmt.Errorf("activate model: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit model activation: %w", err)
	}
	return nil
}
