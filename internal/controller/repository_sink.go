package controller

import (
	"context"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

// RepositorySink adapts controller observations to the persistence contract.
// Invalid candidates are intentionally absent from Desired; Reconcile then
// deactivates a formerly active workload with the same parent/trigger.
type RepositorySink struct {
	Repository workloadstore.Repository
}

func (s RepositorySink) Observe(ctx context.Context, observation Observation) error {
	desired := make([]domain.WorkloadSpec, 0, len(observation.Result.Candidates))
	for _, candidate := range observation.Result.Candidates {
		if candidate.Spec != nil {
			desired = append(desired, *candidate.Spec)
		}
	}
	return s.Repository.Reconcile(ctx, workloadstore.Reconciliation{
		Parent:             parentKey(observation.Source),
		ScaledObjectUID:    observation.Source.UID,
		ObservedGeneration: observation.Source.ObservedGeneration,
		Desired:            desired,
	})
}

func (s RepositorySink) DeactivateScaledObject(ctx context.Context, reference ScaledObjectReference) error {
	return s.Repository.DeactivateParent(ctx, parentKey(reference))
}

func parentKey(reference ScaledObjectReference) workloadstore.ParentKey {
	return workloadstore.ParentKey{
		ClusterID:        reference.ClusterID,
		Namespace:        reference.Namespace,
		ScaledObjectName: reference.ScaledObjectName,
	}
}
