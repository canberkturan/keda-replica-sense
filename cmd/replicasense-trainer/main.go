package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/canberkturan/keda-replica-sense/internal/trainer"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cluster, databaseURL, workloadID, runID := os.Getenv("REPLICASENSE_CLUSTER_ID"), os.Getenv("REPLICASENSE_DATABASE_URL"), os.Getenv("REPLICASENSE_WORKLOAD_ID"), os.Getenv("REPLICASENSE_TRAINING_RUN_ID")
	if cluster == "" || databaseURL == "" || workloadID == "" || runID == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_CLUSTER_ID, REPLICASENSE_DATABASE_URL, REPLICASENSE_WORKLOAD_ID, and REPLICASENSE_TRAINING_RUN_ID are required")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create PostgreSQL pool:", err)
		os.Exit(1)
	}
	defer pool.Close()
	repository := postgres.NewSampleRepository(pool)
	service := trainer.Service{Workloads: repository, Samples: repository, Runs: postgres.NewTrainingRepository(pool), Models: postgres.NewModelRepository(pool)}
	if err := service.Run(ctx, cluster, workloadID, runID, os.Getenv("REPLICASENSE_MODEL_ENGINE"), time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "train:", err)
		os.Exit(1)
	}
}
