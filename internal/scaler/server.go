// Package scaler implements the KEDA external-scaler gRPC boundary.
package scaler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	externalscaler "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	"google.golang.org/grpc"
)

const MetricName = "replicasense_predictive"

// Server is deliberately fail-closed until the forecaster provides a fresh,
// safety-approved snapshot. Native KEDA triggers remain fully independent.
type Server struct {
	externalscaler.UnimplementedExternalScalerServer
	ClusterID      string
	SnapshotReader interface {
		LatestSnapshot(context.Context, forecast.Lookup) (*domain.ForecastSnapshot, error)
	}
	SnapshotMaxAge time.Duration
	Observer       interface {
		RecordRequest(method string, served bool, age time.Duration)
	}
}

func (s Server) IsActive(ctx context.Context, reference *externalscaler.ScaledObjectRef) (*externalscaler.IsActiveResponse, error) {
	snapshot, ok := s.snapshot(ctx, reference)
	s.record("is_active", snapshot, ok)
	return &externalscaler.IsActiveResponse{Result: ok && snapshot.SafeDemand > 0}, nil
}

func (Server) GetMetricSpec(_ context.Context, reference *externalscaler.ScaledObjectRef) (*externalscaler.GetMetricSpecResponse, error) {
	if reference == nil || strings.TrimSpace(reference.GetName()) == "" || strings.TrimSpace(reference.GetNamespace()) == "" {
		return nil, fmt.Errorf("ScaledObject name and namespace are required")
	}
	return &externalscaler.GetMetricSpecResponse{MetricSpecs: []*externalscaler.MetricSpec{{MetricName: MetricName, TargetSizeFloat: 1}}}, nil
}

func (s Server) GetMetrics(ctx context.Context, request *externalscaler.GetMetricsRequest) (*externalscaler.GetMetricsResponse, error) {
	if request == nil || request.GetScaledObjectRef() == nil {
		return nil, fmt.Errorf("ScaledObject reference is required")
	}
	if request.GetMetricName() != MetricName {
		return nil, fmt.Errorf("unknown metric %q", request.GetMetricName())
	}
	snapshot, ok := s.snapshot(ctx, request.GetScaledObjectRef())
	s.record("get_metrics", snapshot, ok)
	value := float64(0)
	if ok {
		value = snapshot.SafeDemand
	}
	return &externalscaler.GetMetricsResponse{MetricValues: []*externalscaler.MetricValue{{MetricName: MetricName, MetricValueFloat: value}}}, nil
}

func (s Server) record(method string, snapshot *domain.ForecastSnapshot, served bool) {
	if s.Observer == nil {
		return
	}
	age := time.Duration(0)
	if snapshot != nil {
		age = time.Since(snapshot.GeneratedAt)
	}
	s.Observer.RecordRequest(method, served, age)
}

func (s Server) snapshot(ctx context.Context, reference *externalscaler.ScaledObjectRef) (*domain.ForecastSnapshot, bool) {
	if s.SnapshotReader == nil || reference == nil || s.ClusterID == "" {
		return nil, false
	}
	source := strings.TrimSpace(reference.GetScalerMetadata()["sourceTrigger"])
	if source == "" {
		return nil, false
	}
	snapshot, err := s.SnapshotReader.LatestSnapshot(ctx, forecast.Lookup{ClusterID: s.ClusterID, Namespace: reference.GetNamespace(), ScaledObject: reference.GetName(), SourceTrigger: source})
	if err != nil {
		return nil, false
	}
	maxAge := s.SnapshotMaxAge
	if maxAge <= 0 {
		maxAge = 2 * time.Minute
	}
	return snapshot, snapshotFresh(snapshot.GeneratedAt, time.Now(), maxAge)
}

// snapshotFresh rejects both stale and future-dated snapshots. Accepting a
// future timestamp would turn a clock-skew or corrupt database row into an
// indefinitely fresh predictive recommendation.
func snapshotFresh(generatedAt, now time.Time, maxAge time.Duration) bool {
	if generatedAt.After(now) {
		return false
	}
	return now.Sub(generatedAt) <= maxAge
}

func (Server) StreamIsActive(_ *externalscaler.ScaledObjectRef, stream grpc.ServerStreamingServer[externalscaler.IsActiveResponse]) error {
	return stream.Send(&externalscaler.IsActiveResponse{Result: false})
}
