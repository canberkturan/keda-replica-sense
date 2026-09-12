package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ForecastRepository struct{ database *pgxpool.Pool }

func NewForecastRepository(pool *pgxpool.Pool) *ForecastRepository {
	return &ForecastRepository{database: pool}
}

func (r *ForecastRepository) SaveSnapshot(ctx context.Context, s domain.ForecastSnapshot) error {
	_, err := r.database.Exec(ctx, `INSERT INTO forecast_snapshots (cluster_id,workload_id,source_fingerprint,generated_at,observed_at,model_engine,forecast_horizon_seconds,forecast_p50,forecast_p95,surge_demand,safe_demand,safety_reason) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, s.ClusterID, s.WorkloadID, s.SourceFingerprint, s.GeneratedAt, s.ObservedAt, s.ModelEngine, int64(s.ForecastHorizon/time.Second), s.ForecastP50, s.ForecastP95, s.SurgeDemand, s.SafeDemand, s.SafetyReason)
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

func (r *ForecastRepository) LatestSnapshot(ctx context.Context, l forecast.Lookup) (*domain.ForecastSnapshot, error) {
	var s domain.ForecastSnapshot
	var seconds int64
	err := r.database.QueryRow(ctx, `SELECT f.cluster_id,f.workload_id,f.source_fingerprint,f.generated_at,f.observed_at,f.model_engine,f.forecast_horizon_seconds,f.forecast_p50,f.forecast_p95,f.surge_demand,f.safe_demand,f.safety_reason FROM forecast_snapshots f JOIN workloads w ON w.id=f.workload_id WHERE w.cluster_id=$1 AND w.namespace=$2 AND w.scaled_object_name=$3 AND w.source_trigger_name=$4 AND w.status='active' ORDER BY f.generated_at DESC LIMIT 1`, l.ClusterID, l.Namespace, l.ScaledObject, l.SourceTrigger).Scan(&s.ClusterID, &s.WorkloadID, &s.SourceFingerprint, &s.GeneratedAt, &s.ObservedAt, &s.ModelEngine, &seconds, &s.ForecastP50, &s.ForecastP95, &s.SurgeDemand, &s.SafeDemand, &s.SafetyReason)
	if err != nil {
		return nil, err
	}
	s.ForecastHorizon = time.Duration(seconds) * time.Second
	return &s, nil
}

func (r *ForecastRepository) ListLatestSnapshots(ctx context.Context, clusterID string) ([]forecast.SnapshotWithLookup, error) {
	rows, err := r.database.Query(ctx, `SELECT DISTINCT ON (f.workload_id)
		w.namespace,w.scaled_object_name,w.source_trigger_name,
		f.cluster_id,f.workload_id,f.source_fingerprint,f.generated_at,f.observed_at,f.model_engine,f.forecast_horizon_seconds,f.forecast_p50,f.forecast_p95,f.surge_demand,f.safe_demand,f.safety_reason
		FROM forecast_snapshots f JOIN workloads w ON w.id=f.workload_id
		WHERE w.cluster_id=$1 AND w.status='active'
		ORDER BY f.workload_id,f.generated_at DESC`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list latest snapshots: %w", err)
	}
	defer rows.Close()
	var results []forecast.SnapshotWithLookup
	for rows.Next() {
		var result forecast.SnapshotWithLookup
		var seconds int64
		if err := rows.Scan(&result.Lookup.Namespace, &result.Lookup.ScaledObject, &result.Lookup.SourceTrigger,
			&result.Snapshot.ClusterID, &result.Snapshot.WorkloadID, &result.Snapshot.SourceFingerprint, &result.Snapshot.GeneratedAt, &result.Snapshot.ObservedAt, &result.Snapshot.ModelEngine, &seconds, &result.Snapshot.ForecastP50, &result.Snapshot.ForecastP95, &result.Snapshot.SurgeDemand, &result.Snapshot.SafeDemand, &result.Snapshot.SafetyReason); err != nil {
			return nil, fmt.Errorf("scan latest snapshot: %w", err)
		}
		result.Lookup.ClusterID = clusterID
		result.Snapshot.ForecastHorizon = time.Duration(seconds) * time.Second
		results = append(results, result)
	}
	return results, rows.Err()
}

func (r *ForecastRepository) ProjectedSpeculativeReplicas(ctx context.Context, clusterID, workloadID string, desired float64, minReplicas int32) (float64, error) {
	rows, err := r.database.Query(ctx, `SELECT w.id,w.min_replica_count,f.safe_demand
		FROM workloads w JOIN LATERAL (
			SELECT safe_demand FROM forecast_snapshots f WHERE f.workload_id=w.id AND f.source_fingerprint=w.source_fingerprint ORDER BY generated_at DESC LIMIT 1
		) f ON true WHERE w.cluster_id=$1 AND w.status='active'`, clusterID)
	if err != nil {
		return 0, fmt.Errorf("list speculative demand: %w", err)
	}
	defer rows.Close()
	var total float64
	for rows.Next() {
		var id string
		var minimum int32
		var safe float64
		if err := rows.Scan(&id, &minimum, &safe); err != nil {
			return 0, fmt.Errorf("scan speculative demand: %w", err)
		}
		if id == workloadID {
			safe, minimum = desired, minReplicas
		}
		if safe > float64(minimum) {
			total += safe - float64(minimum)
		}
	}
	return total, rows.Err()
}
