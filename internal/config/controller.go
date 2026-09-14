// Package config loads process configuration for ReplicaSense components.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
)

type Controller struct {
	Parser                    scaledobject.ParserOptions
	MetricsBindAddress        string
	HealthProbeBindAddress    string
	LeaderElection            bool
	LeaderElectionID          string
	LeaderElectionNamespace   string
	SystemNamespace           string
	TrainerJobCleanupInterval time.Duration
	DatabaseURL               string
}

// LoadController reads controller configuration from an environment lookup
// function. Passing os.Getenv in main and a small lookup closure in tests keeps
// configuration parsing deterministic and testable.
func LoadController(lookup func(string) string) (Controller, error) {
	clusterID := strings.TrimSpace(lookup("REPLICASENSE_CLUSTER_ID"))
	if clusterID == "" {
		return Controller{}, fmt.Errorf("REPLICASENSE_CLUSTER_ID is required")
	}

	addresses, err := parseCommaSeparatedSet(lookup("REPLICASENSE_SCALER_ADDRESSES"))
	if err != nil {
		return Controller{}, fmt.Errorf("REPLICASENSE_SCALER_ADDRESSES: %w", err)
	}
	databaseURL := strings.TrimSpace(lookup("REPLICASENSE_DATABASE_URL"))
	if databaseURL == "" {
		return Controller{}, fmt.Errorf("REPLICASENSE_DATABASE_URL is required")
	}
	leaderElection, err := parseBoolWithDefault(lookup("REPLICASENSE_LEADER_ELECTION"), true)
	if err != nil {
		return Controller{}, fmt.Errorf("REPLICASENSE_LEADER_ELECTION: %w", err)
	}

	leaderElectionNamespace := strings.TrimSpace(lookup("REPLICASENSE_LEADER_ELECTION_NAMESPACE"))
	systemNamespace := valueOrDefault(lookup("REPLICASENSE_SYSTEM_NAMESPACE"), leaderElectionNamespace)
	if systemNamespace == "" {
		systemNamespace = "replicasense-system"
	}
	cleanupInterval, err := durationWithDefault(lookup("REPLICASENSE_TRAINER_JOB_CLEANUP_INTERVAL"), 24*time.Hour)
	if err != nil {
		return Controller{}, fmt.Errorf("REPLICASENSE_TRAINER_JOB_CLEANUP_INTERVAL: %w", err)
	}

	return Controller{
		Parser: scaledobject.ParserOptions{
			ClusterID:              clusterID,
			ScalerAddresses:        addresses,
			DefaultMinReplicaCount: 0,
			DefaultMaxReplicaCount: 100,
		},
		MetricsBindAddress:        valueOrDefault(lookup("REPLICASENSE_METRICS_BIND_ADDRESS"), ":8080"),
		HealthProbeBindAddress:    valueOrDefault(lookup("REPLICASENSE_HEALTH_PROBE_BIND_ADDRESS"), ":8081"),
		LeaderElection:            leaderElection,
		LeaderElectionID:          valueOrDefault(lookup("REPLICASENSE_LEADER_ELECTION_ID"), "replicasense-controller.keda.sh"),
		LeaderElectionNamespace:   leaderElectionNamespace,
		SystemNamespace:           systemNamespace,
		TrainerJobCleanupInterval: cleanupInterval,
		DatabaseURL:               databaseURL,
	}, nil
}

func durationWithDefault(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("must be a positive duration")
	}
	return value, nil
}

func parseCommaSeparatedSet(raw string) (map[string]struct{}, error) {
	values := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			values[item] = struct{}{}
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("must contain at least one scaler address")
	}
	return values, nil
}

func parseBoolWithDefault(raw string, defaultValue bool) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("must be true or false")
	}
	return value, nil
}

func valueOrDefault(value, defaultValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue
	}
	return value
}
