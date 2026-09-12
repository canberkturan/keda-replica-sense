package sampler

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestBuildSampleUsesWorkloadAndQueryTime(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 12, 34, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	workload := workloadstore.SamplingWorkload{
		ID: "a04f1a11-24ec-4f84-bcaf-5a6263356ae5",
		Spec: domain.WorkloadSpec{
			Key:               domain.WorkloadKey{ClusterID: "cluster-a"},
			SourceFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}

	sample := BuildSample(workload, observedAt, 42.5)
	if sample.ClusterID != "cluster-a" || sample.WorkloadID != workload.ID || sample.SourceFingerprint != workload.Spec.SourceFingerprint {
		t.Fatalf("unexpected sample identity: %#v", sample)
	}
	if sample.ObservedAt.Location() != time.UTC || sample.ObservedAt.Hour() != 9 || sample.ObservedValue != 42.5 {
		t.Fatalf("unexpected sample values: %#v", sample)
	}
}

func TestJitterIsStableAndBounded(t *testing.T) {
	service := &Service{JitterWindow: 15 * time.Second}
	workload := workloadstore.SamplingWorkload{ID: "a04f1a11-24ec-4f84-bcaf-5a6263356ae5", Spec: domain.WorkloadSpec{Forecast: domain.ForecastConfig{SamplingInterval: time.Minute}}}
	first, second := service.jitterFor(workload), service.jitterFor(workload)
	if first != second {
		t.Fatalf("jitter changed across calls: %s != %s", first, second)
	}
	if first < 0 || first >= 15*time.Second || first%time.Second != 0 {
		t.Fatalf("jitter %s is outside configured window", first)
	}

	workload.Spec.Forecast.SamplingInterval = 5 * time.Second
	if got := service.jitterFor(workload); got < 0 || got >= 5*time.Second {
		t.Fatalf("jitter %s must be bounded by workload sampling interval", got)
	}
}

func TestServiceIsDueAtStableWorkloadOffset(t *testing.T) {
	service := &Service{JitterWindow: 15 * time.Second}
	workload := workloadstore.SamplingWorkload{ID: "a04f1a11-24ec-4f84-bcaf-5a6263356ae5", Spec: domain.WorkloadSpec{Forecast: domain.ForecastConfig{SamplingInterval: time.Minute}}}
	offset := service.jitterFor(workload)
	cycle := time.Date(2026, 9, 1, 12, 34, 0, 0, time.UTC)
	if !service.isDue(workload, cycle.Add(offset)) {
		t.Fatalf("workload should be due at offset %s", offset)
	}
	if offset > 0 && service.isDue(workload, cycle) {
		t.Fatal("workload must not be due at the start of a different offset")
	}
}
