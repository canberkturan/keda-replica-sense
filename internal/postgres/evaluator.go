package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Evaluator struct{ database *pgxpool.Pool }

func NewEvaluator(pool *pgxpool.Pool) *Evaluator { return &Evaluator{database: pool} }

// EvaluateMatured evaluates forecasts only after their full horizon has passed
// from the observation used as the model's prediction point. GeneratedAt can
// differ from ObservedAt because sampling is deliberately jittered and model
// inference itself takes time.
func (e *Evaluator) EvaluateMatured(ctx context.Context, now time.Time) error {
	rows, err := e.database.Query(ctx, `SELECT id,workload_id,source_fingerprint,observed_at,forecast_horizon_seconds,forecast_p95 FROM forecast_snapshots WHERE evaluated_at IS NULL AND observed_at + forecast_horizon_seconds * interval '1 second' <= $1 ORDER BY observed_at LIMIT 100`, now)
	if err != nil {
		return fmt.Errorf("list matured forecasts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var workloadID, fp string
		var observed time.Time
		var seconds int64
		var p95 float64
		if err := rows.Scan(&id, &workloadID, &fp, &observed, &seconds, &p95); err != nil {
			return err
		}
		var actual *float64
		err := e.database.QueryRow(ctx, `SELECT max(observed_value) FROM samples WHERE workload_id=$1 AND source_fingerprint=$2 AND observed_at > $3 AND observed_at <= $3 + $4 * interval '1 second'`, workloadID, fp, observed, seconds).Scan(&actual)
		if err != nil {
			return err
		}
		if actual == nil {
			continue
		}
		covered := p95 >= *actual
		_, err = e.database.Exec(ctx, `UPDATE forecast_snapshots SET realized_horizon_max=$2,evaluated_at=$3,covered=$4,underpredicted=$5,absolute_error=$6 WHERE id=$1`, id, *actual, now, covered, !covered, abs(p95-*actual))
		if err != nil {
			return fmt.Errorf("update forecast evaluation: %w", err)
		}
	}
	return rows.Err()
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
