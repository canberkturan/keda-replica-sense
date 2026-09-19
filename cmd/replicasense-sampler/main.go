package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/canberkturan/keda-replica-sense/internal/config"
	"github.com/canberkturan/keda-replica-sense/internal/observability"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	sourceprometheus "github.com/canberkturan/keda-replica-sense/internal/prometheus"
	"github.com/canberkturan/keda-replica-sense/internal/sampler"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	configuration, err := config.LoadSampler(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, configuration.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	repository := postgres.NewSampleRepository(pool)
	prometheusClient := sourceprometheus.Client{}
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metrics := observability.NewSamplerMetrics(metricsRegistry)
	observability.StartMetricsServer(ctx, configuration.MetricsListenAddress, metricsRegistry, func(err error) {
		fmt.Fprintln(os.Stderr, "serve metrics:", err)
	})
	service := sampler.Service{Store: repository, Querier: prometheusClient, ClusterID: configuration.ClusterID, MaxConcurrent: configuration.MaxConcurrent, QueryTimeout: configuration.QueryTimeout, MaxRetries: configuration.MaxRetries, InitialBackoff: configuration.InitialBackoff, JitterWindow: configuration.JitterWindow, Backfiller: &sampler.Backfiller{Store: repository, Querier: prometheusClient, QueryTimeout: configuration.QueryTimeout}, Observer: metrics}
	for {
		now := time.Now().UTC()
		if err := service.RunOnce(ctx, now); err != nil {
			metrics.RecordCycle(now, err)
			fmt.Fprintln(os.Stderr, "sample cycle:", err)
		} else {
			metrics.RecordCycle(now, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(configuration.TickInterval):
		}
	}
}
