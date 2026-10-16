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
	rows, err := r.database.Query(ctx, `SELECT id,workload_id,source_fingerprint,engine,artifact_data,greatest(activated_at,(validation_metrics->>'validated_at')::timestamptz) FROM models WHERE cluster_id=$1 AND status='active' ORDER BY activated_at DESC`, clusterID)
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
