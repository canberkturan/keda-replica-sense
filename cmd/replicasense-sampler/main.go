package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	metrics := observability.NewSamplerMetrics(metricsRegistry)
	metricsServer := &http.Server{Addr: configuration.MetricsListenAddress, Handler: promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "serve metrics:", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = metricsServer.Shutdown(shutdownContext)
	}()
	service := sampler.Service{Store: repository, Querier: prometheusClient, ClusterID: configuration.ClusterID, MaxConcurrent: configuration.MaxConcurrent, QueryTimeout: configuration.QueryTimeout, MaxRetries: configuration.MaxRetries, InitialBackoff: configuration.InitialBackoff, JitterWindow: configuration.JitterWindow, Backfiller: &sampler.Backfiller{Store: repository, Querier: prometheusClient, QueryTimeout: configuration.QueryTimeout}, Observer: metrics}
	for {
		if err := service.RunOnce(ctx, time.Now().UTC()); err != nil {
			fmt.Fprintln(os.Stderr, "sample cycle:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(configuration.TickInterval):
		}
	}
}
