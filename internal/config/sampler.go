package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Sampler struct {
	ClusterID            string
	DatabaseURL          string
	TickInterval         time.Duration
	MaxConcurrent        int
	QueryTimeout         time.Duration
	MaxRetries           int
	InitialBackoff       time.Duration
	JitterWindow         time.Duration
	MetricsListenAddress string
}

func LoadSampler(lookup func(string) string) (Sampler, error) {
	clusterID := strings.TrimSpace(lookup("REPLICASENSE_CLUSTER_ID"))
	databaseURL := strings.TrimSpace(lookup("REPLICASENSE_DATABASE_URL"))
	if clusterID == "" || databaseURL == "" {
		return Sampler{}, fmt.Errorf("REPLICASENSE_CLUSTER_ID and REPLICASENSE_DATABASE_URL are required")
	}
	tickInterval, err := durationOrDefault(lookup("REPLICASENSE_SAMPLER_TICK_INTERVAL"), time.Second)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_SAMPLER_TICK_INTERVAL: %w", err)
	}
	timeout, err := durationOrDefault(lookup("REPLICASENSE_PROMETHEUS_QUERY_TIMEOUT"), 10*time.Second)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_PROMETHEUS_QUERY_TIMEOUT: %w", err)
	}
	backoff, err := durationOrDefault(lookup("REPLICASENSE_SAMPLING_INITIAL_BACKOFF"), 500*time.Millisecond)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_SAMPLING_INITIAL_BACKOFF: %w", err)
	}
	jitter, err := durationOrDefault(lookup("REPLICASENSE_SAMPLING_JITTER_WINDOW"), 15*time.Second)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_SAMPLING_JITTER_WINDOW: %w", err)
	}
	concurrency, err := positiveIntOrDefault(lookup("REPLICASENSE_SAMPLING_MAX_CONCURRENT"), 8)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_SAMPLING_MAX_CONCURRENT: %w", err)
	}
	retries, err := nonNegativeIntOrDefault(lookup("REPLICASENSE_SAMPLING_MAX_RETRIES"), 2)
	if err != nil {
		return Sampler{}, fmt.Errorf("REPLICASENSE_SAMPLING_MAX_RETRIES: %w", err)
	}
	return Sampler{ClusterID: clusterID, DatabaseURL: databaseURL, TickInterval: tickInterval, MaxConcurrent: concurrency, QueryTimeout: timeout, MaxRetries: retries, InitialBackoff: backoff, JitterWindow: jitter, MetricsListenAddress: valueOrDefault(lookup("REPLICASENSE_METRICS_LISTEN_ADDRESS"), ":8080")}, nil
}

func durationOrDefault(raw string, defaultValue time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("must be a positive duration")
	}
	return value, nil
}

func positiveIntOrDefault(raw string, defaultValue int) (int, error) {
	value, err := nonNegativeIntOrDefault(raw, defaultValue)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return value, nil
}

func nonNegativeIntOrDefault(raw string, defaultValue int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return value, nil
}
