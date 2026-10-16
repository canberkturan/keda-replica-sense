package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/jackc/pgx/v5"
)

// InsertMissingSamples never updates observed values, including live points
// whose timestamps are jittered within a slot. Fence each bounded chunk against
// workload policy/source changes; no lock is held during Prometheus requests.
func (r *SampleRepository) InsertMissingSamples(ctx context.Context, w workloadstore.SamplingWorkload, samples []domain.Sample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	interval := w.Spec.Forecast.SamplingInterval
	if interval < time.Second || len(samples) > 10000 {
		return 0, fmt.Errorf("invalid history insert batch")
	}
	tx, err := r.database.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var revision, fingerprint string
	if err := tx.QueryRow(ctx, `SELECT policy_revision,source_fingerprint FROM workloads WHERE id=$1 AND cluster_id=$2 AND status='active' FOR SHARE`, w.ID, w.Spec.Key.ClusterID).Scan(&revision, &fingerprint); err != nil {
		return 0, err
	}
	if revision != w.Spec.PolicyRevision || fingerprint != w.Spec.SourceFingerprint {
		return 0, fmt.Errorf("training history policy or source changed")
	}
	batch := &pgx.Batch{}
	for _, p := range samples {
		if p.ClusterID != w.Spec.Key.ClusterID || p.WorkloadID != w.ID || p.SourceFingerprint != fingerprint || math.IsNaN(p.ObservedValue) || math.IsInf(p.ObservedValue, 0) || p.ObservedValue < 0 || !p.ObservedAt.Equal(p.ObservedAt.Truncate(interval)) {
			return 0, fmt.Errorf("invalid history insert identity or value")
		}
		batch.Queue(`INSERT INTO samples(cluster_id,workload_id,source_fingerprint,observed_at,observed_value)
		 SELECT $1,$2,$3,$4,$5 WHERE NOT EXISTS(SELECT 1 FROM samples WHERE cluster_id=$1 AND workload_id=$2 AND source_fingerprint=$3 AND observed_at >= $4 AND observed_at < $4 + $6 * interval '1 second')
		 ON CONFLICT(cluster_id,workload_id,source_fingerprint,observed_at) DO NOTHING`, p.ClusterID, p.WorkloadID, p.SourceFingerprint, p.ObservedAt, p.ObservedValue, int64(interval/time.Second))
	}
	results := tx.SendBatch(ctx, batch)
	inserted := 0
	for range samples {
		tag, err := results.Exec()
		if err != nil {
			_ = results.Close()
			return 0, err
		}
		inserted += int(tag.RowsAffected())
	}
	if err := results.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}
