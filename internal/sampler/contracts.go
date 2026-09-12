// Package sampler contains the cluster-local sampling pipeline.
package sampler

import (
	"context"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

// PrometheusQuerier is the only ReplicaSense abstraction allowed to read a
// metric source. Trainer, forecaster, and scaler packages must not depend on it.
type PrometheusQuerier interface {
	QueryInstant(context.Context, PrometheusQuery) (float64, error)
}

type PrometheusQuery struct {
	ServerAddress string
	Query         string
	ObservedAt    time.Time
}

// Store is intentionally the narrow sampling view of PostgreSQL.
type Store interface {
	workloadstore.SamplingRepository
}

// BuildSample converts a successful instant query result into the exact sample
// identity persisted by the sampler. The scheduler will supply ObservedAt in a
// later M4 increment, enabling deterministic jitter and retries.
func BuildSample(workload workloadstore.SamplingWorkload, observedAt time.Time, value float64) domain.Sample {
	return domain.Sample{
		ClusterID:         workload.Spec.Key.ClusterID,
		WorkloadID:        workload.ID,
		SourceFingerprint: workload.Spec.SourceFingerprint,
		ObservedAt:        observedAt.UTC(),
		ObservedValue:     value,
	}
}
