// Package workloadstore defines persistence contracts for predictive workload
// configuration. Implementations belong to infrastructure packages, not here.
package workloadstore

import (
	"context"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// ParentKey identifies the ScaledObject that owns one or more predictive
// triggers. It is also sufficient to deactivate all child workloads following
// a Kubernetes deletion event.
type ParentKey struct {
	ClusterID        string
	Namespace        string
	ScaledObjectName string
}

// Reconciliation represents the complete desired state for one parent
// ScaledObject. Desired contains valid predictive triggers only. A repository
// must deactivate previously active child workloads that are absent from it,
// including children made invalid by a configuration update.
type Reconciliation struct {
	Parent             ParentKey
	ScaledObjectUID    string
	ObservedGeneration int64
	Desired            []domain.WorkloadSpec
}

type Repository interface {
	Reconcile(context.Context, Reconciliation) error
	DeactivateParent(context.Context, ParentKey) error
}

// SamplingWorkload is the active configuration the cluster-local sampler needs
// to read one metric source. The internal ID, rather than the logical key, is
// used in sample rows so a renamed workload cannot join old observations.
type SamplingWorkload struct {
	ID   string
	Spec domain.WorkloadSpec
}

// SamplingRepository owns reads of active workload contracts and idempotent
// writes of their canonical observations.
type SamplingRepository interface {
	ListActiveForCluster(context.Context, string) ([]SamplingWorkload, error)
	UpsertSamples(context.Context, []domain.Sample) error
}

type SampleCoverage struct {
	OldestObservedAt time.Time
	NewestObservedAt time.Time
	Count            int64
}

type HistoryRepository interface {
	SamplingRepository
	GetSampleCoverage(context.Context, SamplingWorkload, time.Time, time.Time) (SampleCoverage, error)
}

type DatasetRepository interface {
	ListSamplesForWindow(context.Context, SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error)
}
