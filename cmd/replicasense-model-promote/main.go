package main

import (
	"context"
	"fmt"
	"os"

	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// replicasense-model-promote is intentionally explicit. Candidate activation
// is an operator decision until automatic baseline-comparison gates exist.
func main() {
	databaseURL, modelID := os.Getenv("REPLICASENSE_DATABASE_URL"), os.Getenv("REPLICASENSE_MODEL_ID")
	if databaseURL == "" || modelID == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_DATABASE_URL and REPLICASENSE_MODEL_ID are required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create PostgreSQL pool:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := postgres.NewModelRepository(pool).Activate(context.Background(), modelID); err != nil {
		fmt.Fprintln(os.Stderr, "activate model:", err)
		os.Exit(1)
	}
}
