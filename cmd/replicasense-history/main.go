// replicasense-history is the shared preflight used by the CPU PyTorch image.
// It has no Kubernetes credentials and cannot change model activation state.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/history"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	promsource "github.com/canberkturan/keda-replica-sense/internal/prometheus"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	cluster, workloadID, revision, database := os.Getenv("REPLICASENSE_CLUSTER_ID"), os.Getenv("REPLICASENSE_WORKLOAD_ID"), os.Getenv("REPLICASENSE_POLICY_REVISION"), os.Getenv("REPLICASENSE_DATABASE_URL")
	if cluster == "" || workloadID == "" || revision == "" || database == "" {
		return fmt.Errorf("history repair requires cluster, workload, policy revision and database")
	}
	policy, err := history.PolicyFromEnvironment(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), policy.Timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		return fmt.Errorf("invalid history database configuration")
	}
	defer pool.Close()
	repo := postgres.NewSampleRepository(pool)
	items, err := repo.ListActiveForCluster(ctx, cluster)
	if err != nil {
		return err
	}
	for _, w := range items {
		if w.ID != workloadID {
			continue
		}
		if w.Spec.PolicyRevision != revision {
			return fmt.Errorf("history repair workload policy changed")
		}
		now := time.Now().UTC()
		end := now.Truncate(w.Spec.Forecast.SamplingInterval)
		if raw := os.Getenv("REPLICASENSE_HISTORY_END"); raw != "" {
			end, err = time.Parse(time.RFC3339, raw)
			if err != nil || end.After(now.Truncate(w.Spec.Forecast.SamplingInterval)) || now.Sub(end) > max(10*time.Minute, w.Spec.Forecast.SamplingInterval+time.Minute) {
				return fmt.Errorf("invalid history repair end")
			}
		}
		report, err := (history.Repairer{Store: repo, Querier: promsource.Client{}, Policy: policy}).Repair(ctx, w, end.Add(-w.Spec.Forecast.TrainingWindow), end)
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return err
	}
	return fmt.Errorf("history repair workload is not active")
}
