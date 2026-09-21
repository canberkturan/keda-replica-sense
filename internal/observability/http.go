package observability

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// StartMetricsServer starts a small, hardened Prometheus endpoint and ties its
// lifecycle to ctx. Keeping this plumbing in one place gives every long-lived
// ReplicaSense component the same timeout and graceful-shutdown behavior.
func StartMetricsServer(ctx context.Context, address string, gatherer prometheus.Gatherer, reportError func(error)) *http.Server {
	server := &http.Server{
		Addr:              address,
		Handler:           promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) && reportError != nil {
			reportError(err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	return server
}
