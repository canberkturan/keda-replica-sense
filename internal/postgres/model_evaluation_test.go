package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in against a disposable PostgreSQL instance. Every test owns a unique
// schema; never point this at the application database role in production.
func TestModelEvaluationTransactions(t *testing.T) {
	url := os.Getenv("REPLICASENSE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set REPLICASENSE_TEST_DATABASE_URL for PostgreSQL transaction tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "rs_evaluation_" + time.Now().UTC().Format("20060102150405_000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "migrations", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	policy := strings.Repeat("b", 64)
	var workloadID string
	err = pool.QueryRow(ctx, `INSERT INTO workloads(cluster_id,namespace,scaled_object_name,predictive_trigger_name,scaled_object_uid,observed_generation,scale_target_name,min_replica_count,max_replica_count,source_trigger_name,prometheus_server_address,prometheus_query,reactive_threshold,source_fingerprint,forecast_horizon_seconds,forecast_quantile,training_window_seconds,sampling_interval_seconds,model_engine,business_timezone,policy_revision) VALUES('test','test','test','predictive','uid',1,'test',1,10,'reactive','http://prometheus:9090','metric',500,$1,600,.97,2592000,60,'seasonal-baseline','UTC',$2) RETURNING id`, fingerprint, policy).Scan(&workloadID)
	if err != nil {
		t.Fatal(err)
	}
	w := workloadstore.SamplingWorkload{ID: workloadID, Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "test"}, PolicyRevision: policy, SourceFingerprint: fingerprint, Forecast: domain.ForecastConfig{ModelEngine: "seasonal-baseline", Horizon: 10 * time.Minute, SamplingInterval: time.Minute, Quantile: .97, BusinessTimezone: "UTC"}}}
	repository := NewModelRepository(pool)
	t.Run("training_history_HTTP_repair", func(t *testing.T) { testTrainingHistoryRepair(t, NewSampleRepository(pool), w) })
	contract := training.ContractFor(w, 24*time.Hour)
	create := func() training.EvaluationModel {
		raw := []byte(`{"engine":"seasonal-baseline"}`)
		digest := sha256.Sum256(raw)
		id, err := repository.CreateCandidate(ctx, training.ModelCandidate{ClusterID: "test", WorkloadID: workloadID, SourceFingerprint: fingerprint, Engine: "seasonal-baseline", FeatureSchema: "v1", TrainingWindowStart: time.Now().Add(-30 * 24 * time.Hour), TrainingWindowEnd: time.Now().Add(-48 * time.Hour), DatasetFingerprint: fingerprint, Hyperparameters: training.EncodeContract(contract), ValidationMetrics: []byte(`{}`), Artifact: raw, ArtifactSHA256: hex.EncodeToString(digest[:])})
		if err != nil {
			t.Fatal(err)
		}
		return training.EvaluationModel{StoredModel: forecast.StoredModel{ID: id, WorkloadID: workloadID, SourceFingerprint: fingerprint, Engine: "seasonal-baseline", Artifact: raw}, Status: "candidate", Contract: contract}
	}
	report := training.EvaluationReport{Version: training.EvaluationVersion, At: time.Now().UTC(), Action: "promote", Contract: contract, Policy: training.DefaultEvaluationPolicy(), Decision: training.PromotionDecision{Promote: true}}
	report.Evidence.Fingerprint = fingerprint
	report.Evidence.Start = time.Now().Add(-47 * time.Hour)
	report.Evidence.End = time.Now().Add(-time.Minute)
	report.Evidence.Metrics = map[string]forecast.ValidationMetrics{"candidate": {Windows: 100, Coverage: 1, PinballLossP95: 1}, "baseline": {Windows: 100, Coverage: 1, PinballLossP95: 3}, "champion": {Windows: 100, Coverage: 1, PinballLossP95: 2}}
	first := create()
	if err := repository.CompleteEvaluation(ctx, w, first, report); err != nil {
		t.Fatal("activate first:", err)
	}
	second := create()
	report.ExpectedActiveID = first.ID
	report.PreviousModelID = first.ID
	if err := repository.CompleteEvaluation(ctx, w, second, report); err != nil {
		t.Fatal("activate second:", err)
	}
	third := create()
	if err := repository.CompleteEvaluation(ctx, w, third, report); err == nil {
		t.Fatal("stale active ID must fence activation")
	}
	report.ExpectedActiveID = second.ID
	report.Contract.PolicyRevision = "old"
	if err := repository.CompleteEvaluation(ctx, w, third, report); err == nil {
		t.Fatal("incompatible policy must fence activation")
	}
	report.Contract = contract
	report.Action = "shadow"
	if _, err := pool.Exec(ctx, `UPDATE workloads SET policy_revision=$2 WHERE id=$1`, workloadID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteEvaluation(ctx, w, third, report); err == nil {
		t.Fatal("changed workload row must fence stale evaluation")
	}
	if _, err := pool.Exec(ctx, `UPDATE workloads SET policy_revision=$2 WHERE id=$1`, workloadID, policy); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteEvaluation(ctx, w, third, report); err != nil {
		t.Fatal(err)
	}
	var active string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM models WHERE status='active'`).Scan(&active); err != nil || active != second.ID {
		t.Fatal("shadow switched active model", active, err)
	}
	report.Action = "rollback"
	if _, err := pool.Exec(ctx, `UPDATE models SET activated_at=now()-interval '49 hours' WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	report.Evidence.Metrics["candidate"] = forecast.ValidationMetrics{Windows: 100, Coverage: .5, UnderpredictionRate: .5, PinballLossP95: 10}
	report.Evidence.Metrics["previous"] = forecast.ValidationMetrics{Windows: 100, Coverage: 1, PinballLossP95: .5}
	report.Policy.Rollback = true
	report.PreviousModelID = second.ID
	if err := repository.CompleteEvaluation(ctx, w, first, report); err != nil {
		t.Fatal("fresh fenced rollback:", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM models WHERE status='active'`).Scan(&active); err != nil || active != first.ID {
		t.Fatal("rollback failed", active, err)
	}
	// Audit size stays bounded across repeated pending evaluations.
	report.ExpectedActiveID = first.ID
	report.Action = "withheld"
	for i := 0; i < 25; i++ {
		if err := repository.CompleteEvaluation(ctx, w, third, report); err != nil {
			t.Fatal(err)
		}
	}
	var size int
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length(validation_metrics->'evaluation_history') FROM models WHERE id=$1`, third.ID).Scan(&size); err != nil || size != 20 {
		t.Fatal("unbounded audit", size, err)
	}
	release, err := repository.AcquireEvaluation(ctx, workloadID)
	if err != nil || release == nil {
		t.Fatal("lock", err)
	}
	other, err := repository.AcquireEvaluation(ctx, workloadID)
	if err != nil || other != nil {
		t.Fatal("concurrent worker acquired lock", err)
	}
	release()
	other, err = repository.AcquireEvaluation(ctx, workloadID)
	if err != nil || other == nil {
		t.Fatal("lock not released", err)
	}
	other()
	versions, err := repository.EvaluationModels(ctx, w)
	if err != nil || len(versions) != 3 {
		t.Fatal("load model contracts", len(versions), err)
	}
	stored, err := repository.ListActiveModels(ctx, "test")
	if err != nil || len(stored) != 1 {
		t.Fatal("cache model query", err)
	}
	report.Action = "rejected"
	if err := repository.CompleteEvaluation(ctx, w, third, report); err != nil {
		t.Fatal(err)
	}
	fourth := create()
	fifth := create()
	versions, err = repository.EvaluationModels(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		if v.Status == "candidate" && v.ID != fourth.ID {
			t.Fatal("daily training reset pending evaluation clock", v.ID)
		}
	}
	// Two independent workers holding stale reports cannot both switch models.
	report.Action = "promote"
	report.PreviousModelID = first.ID
	report.Evidence.Metrics["candidate"] = forecast.ValidationMetrics{Windows: 100, Coverage: 1, PinballLossP95: 1}
	var successes atomic.Int32
	var group sync.WaitGroup
	for _, candidate := range []training.EvaluationModel{fourth, fifth} {
		group.Add(1)
		go func(target training.EvaluationModel) {
			defer group.Done()
			if repository.CompleteEvaluation(ctx, w, target, report) == nil {
				successes.Add(1)
			}
		}(candidate)
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatal("competing stale reports did not serialize", successes.Load())
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM models WHERE status='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE models SET hyperparameters='{}'::jsonb WHERE id::text=$1`, active); err != nil {
		t.Fatal(err)
	}
	report.ExpectedActiveID = active
	report.Action = "revalidated"
	legacy := training.EvaluationModel{StoredModel: forecast.StoredModel{ID: active}}
	if err := repository.CompleteEvaluation(ctx, w, legacy, report); err != nil {
		t.Fatal("fresh legacy certification", err)
	}
	var lease string
	if err := pool.QueryRow(ctx, `SELECT validation_metrics->>'validated_at' FROM models WHERE id::text=$1`, active).Scan(&lease); err != nil || lease == "" {
		t.Fatal("missing certified lease", err)
	}
	report.Evidence.End = time.Now().Add(-2 * time.Hour)
	if err := repository.CompleteEvaluation(ctx, w, legacy, report); err == nil {
		t.Fatal("old windows renewed active model validity")
	}
}
