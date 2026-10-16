package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/jackc/pgx/v5"
)

func (r *ModelRepository) AcquireEvaluation(ctx context.Context, workloadID string) (func(), error) {
	conn, err := r.database.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('replicasense-evaluation:' || $1,0))`, workloadID).Scan(&acquired); err != nil {
		conn.Release()
		return nil, err
	}
	if !acquired {
		conn.Release()
		return nil, nil
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended('replicasense-evaluation:' || $1,0))`, workloadID).Scan(&unlocked); err != nil || !unlocked {
			// Never return a session holding an advisory lock to the pool.
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}, nil
}

// Preserve the oldest pending candidate's shadow clock across daily training.
// Rejected/incompatible artifacts remain auditable but cannot block the queue.
func (r *ModelRepository) EvaluationModels(ctx context.Context, w workloadstore.SamplingWorkload) ([]training.EvaluationModel, error) {
	rows, err := r.database.Query(ctx, `SELECT DISTINCT ON (status) id,workload_id,source_fingerprint,engine,artifact_data,coalesce(activated_at,'epoch'),status,training_window_end,created_at,hyperparameters,validation_metrics,artifact_sha256
		FROM models m WHERE cluster_id=$1 AND workload_id=$2 AND source_fingerprint=$3
		AND (status<>'candidate' OR (
		 created_at>coalesce((SELECT max(a.created_at) FROM models a WHERE a.cluster_id=$1 AND a.workload_id=$2 AND a.source_fingerprint=$3 AND a.status='active'),'epoch')
		 AND coalesce(validation_metrics->'promotion_v2'->>'action','')<>'rejected'
		 AND coalesce(validation_metrics->'promotion_v2'->>'reason','') NOT IN ('incompatible_contract','candidate_expired','invalid_artifact','invalid_training_boundary')))
		ORDER BY status,CASE WHEN status='candidate' THEN created_at END ASC,created_at DESC`, w.Spec.Key.ClusterID, w.ID, w.Spec.SourceFingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []training.EvaluationModel
	for rows.Next() {
		var item training.EvaluationModel
		var hyper, validation []byte
		if err := rows.Scan(&item.ID, &item.WorkloadID, &item.SourceFingerprint, &item.Engine, &item.Artifact, &item.ActivatedAt, &item.Status, &item.TrainedUntil, &item.CreatedAt, &hyper, &validation, &item.ArtifactSHA256); err != nil {
			return nil, err
		}
		var config struct {
			Contract training.EvaluationContract `json:"evaluation_contract"`
		}
		var evidence struct {
			Report training.EvaluationReport `json:"promotion_v2"`
		}
		if err := json.Unmarshal(hyper, &config); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(validation, &evidence); err != nil {
			return nil, err
		}
		item.Contract, item.Report = config.Contract, evidence.Report
		result = append(result, item)
	}
	return result, rows.Err()
}

// CompleteEvaluation fences the policy and active model inside a transaction.
// All statuses retain artifacts. Evidence lives in existing JSONB columns, so
// upgrades require neither destructive schema bootstrapping nor model resets.
func (r *ModelRepository) CompleteEvaluation(ctx context.Context, w workloadstore.SamplingWorkload, target training.EvaluationModel, report training.EvaluationReport) error {
	tx, err := r.database.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var policy, fingerprint, engine string
	err = tx.QueryRow(ctx, `SELECT policy_revision,source_fingerprint,model_engine FROM workloads WHERE id=$1 AND cluster_id=$2 AND status='active' FOR UPDATE`, w.ID, w.Spec.Key.ClusterID).Scan(&policy, &fingerprint, &engine)
	if err != nil {
		return err
	}
	if policy != w.Spec.PolicyRevision || fingerprint != w.Spec.SourceFingerprint || engine != w.Spec.Forecast.ModelEngine {
		return fmt.Errorf("model evaluation policy changed")
	}
	var activeID string
	var activeFittedUntil, activeAt time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,training_window_end,activated_at FROM models WHERE cluster_id=$1 AND workload_id=$2 AND source_fingerprint=$3 AND status='active' FOR UPDATE`, w.Spec.Key.ClusterID, w.ID, fingerprint).Scan(&activeID, &activeFittedUntil, &activeAt)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if activeID != report.ExpectedActiveID {
		return fmt.Errorf("active model changed during evaluation")
	}
	var state string
	var hyper []byte
	var fittedUntil time.Time
	if err := tx.QueryRow(ctx, `SELECT status,hyperparameters,training_window_end FROM models WHERE id=$1 AND cluster_id=$2 AND workload_id=$3 AND source_fingerprint=$4 AND engine=$5 FOR UPDATE`, target.ID, w.Spec.Key.ClusterID, w.ID, fingerprint, engine).Scan(&state, &hyper, &fittedUntil); err != nil {
		return err
	}
	activate := report.Action == "promote" || report.Action == "rollback"
	if (activate || report.Action == "revalidated") && !training.FreshEvaluationEvidence(report, time.Now()) {
		return fmt.Errorf("model switch or validity renewal requires current complete evaluation evidence")
	}
	if report.Action == "revalidated" {
		if state != "active" || report.Contract.PolicyRevision != policy || report.Evidence.Fingerprint == "" || report.Evidence.Start.Before(fittedUntil) || report.Evidence.End.After(report.At) || report.Evidence.End.Sub(report.Evidence.Start) < report.Policy.MinDuration || report.At.Before(time.Now().Add(-10*time.Minute)) || report.At.After(time.Now().Add(time.Minute)) || !training.CanRevalidate(report.Policy, report.Contract.Quantile, report.Evidence.Metrics["candidate"], report.Evidence.Metrics["baseline"]) {
			return fmt.Errorf("model validity renewal requires fresh complete serving-contract evidence")
		}
	}
	if activate {
		var config struct {
			Contract training.EvaluationContract `json:"evaluation_contract"`
		}
		if err := json.Unmarshal(hyper, &config); err != nil {
			return err
		}
		_, pairedChampion := report.Evidence.Metrics["champion"]
		if config.Contract != report.Contract || report.Contract.PolicyRevision != policy || !report.Decision.Promote || report.Policy.Mode != "automatic" || report.Evidence.Fingerprint == "" || report.Evidence.Start.Before(fittedUntil) || (pairedChampion && report.Evidence.Start.Before(activeFittedUntil)) || (report.Action == "rollback" && (report.Evidence.Start.Before(activeAt) || report.Evidence.Start.Before(activeFittedUntil))) || report.Evidence.End.After(report.At) || report.At.Before(time.Now().Add(-10*time.Minute)) || report.At.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("activation requires fresh compatible evidence")
		}
		var reference *forecast.ValidationMetrics
		if value, ok := report.Evidence.Metrics["champion"]; ok {
			reference = &value
		}
		candidate := report.Evidence.Metrics["candidate"]
		policyCheck := report.Policy
		if report.Action == "rollback" {
			current := candidate
			reference = &current
			candidate = report.Evidence.Metrics["previous"]
			policyCheck.MinimumImprovement = max(.10, policyCheck.MinimumImprovement)
		}
		decision := training.EvaluateArtifactPromotion(policyCheck, report.Contract.Quantile, candidate, reference, report.Evidence.Metrics["baseline"])
		if !decision.Promote || report.Evidence.End.Sub(report.Evidence.Start) < report.Policy.MinDuration {
			return fmt.Errorf("activation evidence does not satisfy policy")
		}
		if (report.Action == "promote" && state != "candidate") || (report.Action == "rollback" && (state != "retired" || !report.Policy.Rollback)) {
			return fmt.Errorf("unexpected model activation state")
		}
		if report.Action == "rollback" {
			var previousID string
			if err := tx.QueryRow(ctx, `SELECT coalesce(validation_metrics->'promotion_v2'->>'previous_model_id','') FROM models WHERE id::text=$1`, activeID).Scan(&previousID); err != nil {
				return err
			}
			if previousID != target.ID {
				return fmt.Errorf("rollback target is not the evaluated previous champion")
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE models SET status='retired',retired_at=now() WHERE id::text=$1`, activeID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE models SET status='active',activated_at=now(),retired_at=NULL WHERE id=$1`, target.ID); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	// Keep a bounded audit trail per artifact; IDs/timestamps are not Prometheus
	// labels. Do not overwrite the previous-model link on a withheld evaluation.
	if report.PreviousModelID == "" && target.Report.PreviousModelID != "" {
		report.PreviousModelID = target.Report.PreviousModelID
		raw, err = json.Marshal(report)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE models SET validation_metrics = validation_metrics || jsonb_build_object('promotion_v2',$2::jsonb,'evaluation_history',
		(SELECT coalesce(jsonb_agg(value ORDER BY ord),'[]'::jsonb) FROM jsonb_array_elements(coalesce(validation_metrics->'evaluation_history','[]'::jsonb) || jsonb_build_array($2::jsonb)) WITH ORDINALITY AS a(value,ord) WHERE ord > jsonb_array_length(coalesce(validation_metrics->'evaluation_history','[]'::jsonb))-19)) WHERE id=$1`, target.ID, raw)
	if err != nil {
		return err
	}
	if report.Action == "revalidated" || activate {
		_, err = tx.Exec(ctx, `UPDATE models SET validation_metrics=jsonb_set(validation_metrics,'{validated_at}',to_jsonb($2::text),true) WHERE id=$1`, target.ID, report.At.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
