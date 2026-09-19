package scaler

import (
	"context"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	externalscaler "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
)

func TestServerFailsClosed(t *testing.T) {
	server := Server{}
	active, err := server.IsActive(context.Background(), &externalscaler.ScaledObjectRef{})
	if err != nil || active.GetResult() {
		t.Fatalf("IsActive() = %#v, %v", active, err)
	}
	spec, err := server.GetMetricSpec(context.Background(), &externalscaler.ScaledObjectRef{Name: "api", Namespace: "default"})
	if err != nil || len(spec.GetMetricSpecs()) != 1 || spec.GetMetricSpecs()[0].GetMetricName() != MetricName {
		t.Fatalf("GetMetricSpec() = %#v, %v", spec, err)
	}
	metrics, err := server.GetMetrics(context.Background(), &externalscaler.GetMetricsRequest{ScaledObjectRef: &externalscaler.ScaledObjectRef{}, MetricName: MetricName})
	if err != nil || metrics.GetMetricValues()[0].GetMetricValueFloat() != 0 {
		t.Fatalf("GetMetrics() = %#v, %v", metrics, err)
	}
}

func TestServerServesOnlyFreshSnapshots(t *testing.T) {
	fresh := &domain.ForecastSnapshot{GeneratedAt: time.Now(), SafeDemand: 7}
	server := Server{ClusterID: "cluster-a", SnapshotReader: fakeSnapshots{snapshot: fresh}, SnapshotMaxAge: time.Minute}
	request := &externalscaler.GetMetricsRequest{ScaledObjectRef: &externalscaler.ScaledObjectRef{Name: "api", Namespace: "default", ScalerMetadata: map[string]string{"sourceTrigger": "reactive"}}, MetricName: MetricName}
	metrics, err := server.GetMetrics(context.Background(), request)
	if err != nil || metrics.GetMetricValues()[0].GetMetricValueFloat() != 7 {
		t.Fatalf("GetMetrics() = %#v, %v", metrics, err)
	}
	server.SnapshotReader = fakeSnapshots{snapshot: &domain.ForecastSnapshot{GeneratedAt: time.Now().Add(-2 * time.Minute), SafeDemand: 7}}
	metrics, _ = server.GetMetrics(context.Background(), request)
	if metrics.GetMetricValues()[0].GetMetricValueFloat() != 0 {
		t.Fatalf("stale value = %v", metrics.GetMetricValues()[0].GetMetricValueFloat())
	}
	server.SnapshotReader = fakeSnapshots{snapshot: &domain.ForecastSnapshot{GeneratedAt: time.Now().Add(time.Minute), SafeDemand: 7}}
	metrics, _ = server.GetMetrics(context.Background(), request)
	if metrics.GetMetricValues()[0].GetMetricValueFloat() != 0 {
		t.Fatalf("future-dated value = %v", metrics.GetMetricValues()[0].GetMetricValueFloat())
	}
}

type fakeSnapshots struct{ snapshot *domain.ForecastSnapshot }

func (f fakeSnapshots) SaveSnapshot(context.Context, domain.ForecastSnapshot) error { return nil }
func (f fakeSnapshots) LatestSnapshot(context.Context, forecast.Lookup) (*domain.ForecastSnapshot, error) {
	return f.snapshot, nil
}
