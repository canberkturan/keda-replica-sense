package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/canberkturan/keda-replica-sense/internal/trainer"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cluster, databaseURL, workloadID, runID, policyRevision := os.Getenv("REPLICASENSE_CLUSTER_ID"), os.Getenv("REPLICASENSE_DATABASE_URL"), os.Getenv("REPLICASENSE_WORKLOAD_ID"), os.Getenv("REPLICASENSE_TRAINING_RUN_ID"), os.Getenv("REPLICASENSE_POLICY_REVISION")
	if cluster == "" || databaseURL == "" || workloadID == "" || runID == "" || policyRevision == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_CLUSTER_ID, REPLICASENSE_DATABASE_URL, REPLICASENSE_WORKLOAD_ID, REPLICASENSE_TRAINING_RUN_ID, and REPLICASENSE_POLICY_REVISION are required")
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
	seasonalPeriod, err := seasonalPeriodFromEnvironment(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_SEASONAL_PERIOD_SECONDS:", err)
		os.Exit(1)
	}
	maxGapIntervals, err := positiveIntFromEnvironment(os.Getenv, "REPLICASENSE_MAX_GAP_INTERVALS", 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_MAX_GAP_INTERVALS:", err)
		os.Exit(1)
	}
	service := trainer.Service{Workloads: repository, Samples: repository, Runs: postgres.NewTrainingRepository(pool), Models: postgres.NewModelRepository(pool), ExpectedPolicyRevision: policyRevision, SeasonalPeriod: seasonalPeriod, MaxGapIntervals: maxGapIntervals}
	if err := service.Run(ctx, cluster, workloadID, runID, os.Getenv("REPLICASENSE_MODEL_ENGINE"), time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "train:", err)
		os.Exit(1)
	}
}

func seasonalPeriodFromEnvironment(lookup func(string) string) (time.Duration, error) {
	raw := lookup("REPLICASENSE_SEASONAL_PERIOD_SECONDS")
	if raw == "" {
		return 24 * time.Hour, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return time.Duration(seconds) * time.Second, nil
}

func positiveIntFromEnvironment(lookup func(string) string, name string, fallback int) (int, error) {
	raw := lookup(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return value, nil
}
