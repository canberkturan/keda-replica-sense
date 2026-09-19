package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/kube"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/canberkturan/keda-replica-sense/internal/training"
	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	cluster, databaseURL := os.Getenv("REPLICASENSE_CLUSTER_ID"), os.Getenv("REPLICASENSE_DATABASE_URL")
	if cluster == "" || databaseURL == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_CLUSTER_ID and REPLICASENSE_DATABASE_URL are required")
		os.Exit(1)
	}
	trainerDatabaseSecret := os.Getenv("REPLICASENSE_TRAINER_DATABASE_SECRET")
	if trainerDatabaseSecret == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_TRAINER_DATABASE_SECRET is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create PostgreSQL pool:", err)
		os.Exit(1)
	}
	defer pool.Close()
	clientConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "create Kubernetes config:", err)
		os.Exit(1)
	}
	client, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create Kubernetes client:", err)
		os.Exit(1)
	}
	resources, err := trainerResources(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "trainer resources:", err)
		os.Exit(1)
	}
	activeDeadline, err := positiveInt64OrDefault(os.Getenv("REPLICASENSE_TRAINER_ACTIVE_DEADLINE_SECONDS"), 3600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_TRAINER_ACTIVE_DEADLINE_SECONDS:", err)
		os.Exit(1)
	}
	samples := postgres.NewSampleRepository(pool)
	scheduler := training.Scheduler{
		Workloads: samples, History: samples, Claims: postgres.NewTrainingRepository(pool),
		Jobs:      kube.TrainingJobCreator{Client: client, Namespace: valueOrDefault("REPLICASENSE_SYSTEM_NAMESPACE", "replicasense-system"), Image: valueOrDefault("REPLICASENSE_TRAINER_IMAGE", "replicasense-trainer:latest"), ImagePullPolicy: corev1.PullPolicy(valueOrDefault("REPLICASENSE_TRAINER_IMAGE_PULL_POLICY", "IfNotPresent")), ServiceAccount: valueOrDefault("REPLICASENSE_TRAINER_SERVICE_ACCOUNT", "replicasense-trainer"), DatabaseSecretName: trainerDatabaseSecret, DatabaseSecretKey: valueOrDefault("REPLICASENSE_TRAINER_DATABASE_SECRET_KEY", "url"), Resources: resources, ActiveDeadlineSecs: activeDeadline},
		ClusterID: cluster, TrainingInterval: durationOrDefault("REPLICASENSE_TRAINING_INTERVAL", 24*time.Hour),
	}
	for {
		if err := scheduler.RunOnce(ctx, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "schedule training:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func trainerResources(lookup func(string) string) (corev1.ResourceRequirements, error) {
	requestCPU, err := quantityOrDefault(lookup("REPLICASENSE_TRAINER_REQUEST_CPU"), "500m")
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("REPLICASENSE_TRAINER_REQUEST_CPU: %w", err)
	}
	requestMemory, err := quantityOrDefault(lookup("REPLICASENSE_TRAINER_REQUEST_MEMORY"), "512Mi")
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("REPLICASENSE_TRAINER_REQUEST_MEMORY: %w", err)
	}
	limitCPU, err := quantityOrDefault(lookup("REPLICASENSE_TRAINER_LIMIT_CPU"), "2")
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("REPLICASENSE_TRAINER_LIMIT_CPU: %w", err)
	}
	limitMemory, err := quantityOrDefault(lookup("REPLICASENSE_TRAINER_LIMIT_MEMORY"), "2Gi")
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("REPLICASENSE_TRAINER_LIMIT_MEMORY: %w", err)
	}
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: requestCPU, corev1.ResourceMemory: requestMemory}, Limits: corev1.ResourceList{corev1.ResourceCPU: limitCPU, corev1.ResourceMemory: limitMemory}}, nil
}

func quantityOrDefault(raw, fallback string) (resource.Quantity, error) {
	value := raw
	if value == "" {
		value = fallback
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil || quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("must be a positive Kubernetes quantity")
	}
	return quantity, nil
}

func positiveInt64OrDefault(raw string, fallback int64) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return value, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationOrDefault(name string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
			return duration
		}
	}
	return fallback
}
