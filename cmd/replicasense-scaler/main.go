package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	externalscaler "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	"google.golang.org/grpc"

	"github.com/canberkturan/keda-replica-sense/internal/observability"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	"github.com/canberkturan/keda-replica-sense/internal/scaler"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	address := os.Getenv("REPLICASENSE_SCALER_LISTEN_ADDRESS")
	if address == "" {
		address = ":6000"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	databaseURL := os.Getenv("REPLICASENSE_DATABASE_URL")
	clusterID := os.Getenv("REPLICASENSE_CLUSTER_ID")
	if databaseURL == "" || clusterID == "" {
		fmt.Fprintln(os.Stderr, "REPLICASENSE_DATABASE_URL and REPLICASENSE_CLUSTER_ID are required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	repository := postgres.NewForecastRepository(pool)
	cache := scaler.NewSnapshotCache(clusterID, repository)
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metrics := observability.NewScalerMetrics(metricsRegistry)
	metricsAddress := os.Getenv("REPLICASENSE_METRICS_LISTEN_ADDRESS")
	if metricsAddress == "" {
		metricsAddress = ":8080"
	}
	observability.StartMetricsServer(ctx, metricsAddress, metricsRegistry, func(err error) {
		fmt.Fprintln(os.Stderr, "serve metrics:", err)
	})
	if err := cache.Refresh(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "initial snapshot refresh:", err)
	}
	refreshSnapshots(ctx, cache, snapshotRefreshInterval())
	server := grpc.NewServer()
	externalscaler.RegisterExternalScalerServer(server, scaler.Server{ClusterID: clusterID, SnapshotReader: cache, Observer: metrics})
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func refreshSnapshots(ctx context.Context, cache *scaler.SnapshotCache, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cache.Refresh(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "snapshot refresh:", err)
				}
			}
		}
	}()
}

func snapshotRefreshInterval() time.Duration {
	if raw := os.Getenv("REPLICASENSE_SNAPSHOT_REFRESH_INTERVAL"); raw != "" {
		if interval, err := time.ParseDuration(raw); err == nil && interval > 0 {
			return interval
		}
	}
	return 15 * time.Second
}
