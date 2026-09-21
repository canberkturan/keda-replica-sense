package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecaster"
	"github.com/canberkturan/keda-replica-sense/internal/kube"
	"github.com/canberkturan/keda-replica-sense/internal/observability"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/canberkturan/keda-replica-sense/internal/safety"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	cluster, databaseURL := os.Getenv("REPLICASENSE_CLUSTER_ID"), os.Getenv("REPLICASENSE_DATABASE_URL")
	if cluster == "" || databaseURL == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_CLUSTER_ID and REPLICASENSE_DATABASE_URL are required")
		os.Exit(1)
	}
	period := durationEnv("REPLICASENSE_SEASONAL_PERIOD", 24*time.Hour)
	interval := durationEnv("REPLICASENSE_FORECAST_INTERVAL", time.Minute)
	minimum := intEnv("REPLICASENSE_FORECAST_MIN_SAMPLES", 0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create PostgreSQL pool:", err)
		os.Exit(1)
	}
	defer pool.Close()
	repo := postgres.NewSampleRepository(pool)
	clientConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "create Kubernetes config:", err)
		os.Exit(1)
	}
	kubeClient, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create Kubernetes client:", err)
		os.Exit(1)
	}
	replicaReader := kube.DeploymentReplicaReader{Client: kubeClient}
	healthChecker := kube.ClusterHealthChecker{Client: kubeClient, MaxUnschedulablePods: intEnv("REPLICASENSE_MAX_UNSCHEDULABLE_PODS", 0)}
	capacityGuard := kube.CapacityGuard{Client: kubeClient, HeadroomFraction: headroomFractionEnv("REPLICASENSE_SPECULATIVE_HEADROOM_FRACTION", 0.25)}
	forecastRepository := postgres.NewForecastRepository(pool)
	budgetGuard := forecaster.SpeculativeBudgetGuard{Reader: forecastRepository, ClusterID: cluster, MaxAdditionalReplicas: replicaBudgetEnv("REPLICASENSE_CLUSTER_SPECULATIVE_REPLICA_BUDGET", 50)}
	modelCache := forecaster.NewModelCache(cluster, postgres.NewModelRepository(pool), durationEnv("REPLICASENSE_MODEL_MAX_AGE", 72*time.Hour))
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metrics := observability.NewForecasterMetrics(metricsRegistry)
	capacityGuard.Observer = func(snapshot kube.CapacitySnapshot) {
		metrics.RecordCapacity(cluster, snapshot)
	}
	observability.StartMetricsServer(ctx, valueOrDefault("REPLICASENSE_METRICS_LISTEN_ADDRESS", ":8080"), metricsRegistry, func(err error) {
		fmt.Fprintln(os.Stderr, "serve metrics:", err)
	})
	surgePolicy := safety.DefaultSurgePolicy()
	surgePolicy.SlopeMultiplier = multiplierEnv("REPLICASENSE_SURGE_SLOPE_MULTIPLIER", surgePolicy.SlopeMultiplier)
	surgePolicy.BaselineMultiplier = multiplierEnv("REPLICASENSE_SURGE_BASELINE_MULTIPLIER", surgePolicy.BaselineMultiplier)
	surgePolicy.ConfirmationSamples = positiveIntEnv("REPLICASENSE_SURGE_CONFIRMATION_SAMPLES", surgePolicy.ConfirmationSamples)
	surgePolicy.MaxMultiplier = multiplierEnv("REPLICASENSE_SURGE_MAX_MULTIPLIER", surgePolicy.MaxMultiplier)
	service := forecaster.Service{Samples: repo, Snapshots: forecastRepository, SeasonalPeriod: period, MinimumSamples: minimum, DisableSurgeDetection: boolEnv("REPLICASENSE_DISABLE_SURGE_DETECTION", false), SurgePolicy: surgePolicy, Models: modelCache, CurrentReplicas: func(ctx context.Context, w workloadstore.SamplingWorkload) (float64, error) {
		return replicaReader.CurrentReplicas(ctx, w.Spec.Key.Namespace, w.Spec.ScaleTarget.Name)
	}, ClusterHealthy: healthChecker.Healthy}
	service.PredictiveAllowed = func(ctx context.Context, w workloadstore.SamplingWorkload, current, desired float64) bool {
		return capacityGuard.Allows(ctx, w.Spec.Key.Namespace, w.Spec.ScaleTarget.Name, current, desired) && budgetGuard.Allows(ctx, w, desired)
	}
	service.OnForecast = func(w workloadstore.SamplingWorkload, snapshot domain.ForecastSnapshot) {
		metrics.Record(w, snapshot, time.Now())
	}
	evaluator := postgres.NewEvaluator(pool)
	for {
		now := time.Now()
		cycleErr := false
		if err := modelCache.Refresh(ctx); err != nil {
			cycleErr = true
			fmt.Fprintln(os.Stderr, "refresh active models:", err)
		}
		workloads, err := repo.ListActiveForCluster(ctx, cluster)
		if err == nil {
			for _, w := range workloads {
				if _, err := service.Forecast(ctx, w, now); err != nil {
					cycleErr = true
					fmt.Fprintln(os.Stderr, "forecast:", err)
				}
			}
		} else {
			cycleErr = true
			fmt.Fprintln(os.Stderr, "list workloads:", err)
		}
		if err := evaluator.EvaluateMatured(ctx, now); err != nil {
			cycleErr = true
			fmt.Fprintln(os.Stderr, "evaluate forecasts:", err)
		}
		if cycleErr {
			metrics.RecordCycle(now, fmt.Errorf("forecaster cycle failed"))
		} else {
			metrics.RecordCycle(now, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
func durationEnv(name string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
func intEnv(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return fallback
}

func positiveIntEnv(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func boolEnv(name string, fallback bool) bool {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			return value
		}
	}
	return fallback
}

func headroomFractionEnv(name string, fallback float64) float64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value > 0 && value <= 1 {
			return value
		}
	}
	return fallback
}

func multiplierEnv(name string, fallback float64) float64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value > 1 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	}
	return fallback
}

// replicaBudgetEnv accepts an absolute replica count. It intentionally does
// not share the fractional headroom parser: a budget such as 100 is valid.
func replicaBudgetEnv(name string, fallback float64) float64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	}
	return fallback
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
