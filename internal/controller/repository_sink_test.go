package controller

import (
	"context"
	"testing"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestRepositorySinkStoresOnlyValidCandidates(t *testing.T) {
	repository := &recordingRepository{}
	sink := RepositorySink{Repository: repository}
	valid := domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "cluster-a", Namespace: "default", ScaledObjectName: "api", PredictiveTriggerName: "predictive"}}
	err := sink.Observe(context.Background(), Observation{
		Source: ScaledObjectReference{ClusterID: "cluster-a", Namespace: "default", ScaledObjectName: "api", UID: "uid-1", ObservedGeneration: 3},
		Result: scaledobject.ParseResult{Candidates: []scaledobject.Candidate{
			{PredictiveTriggerName: "predictive", Spec: &valid},
			{PredictiveTriggerName: "broken"},
		}},
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if len(repository.reconciliations) != 1 || len(repository.reconciliations[0].Desired) != 1 {
		t.Fatalf("unexpected reconciliations: %#v", repository.reconciliations)
	}
	if repository.reconciliations[0].ObservedGeneration != 3 {
		t.Fatalf("generation = %d, want 3", repository.reconciliations[0].ObservedGeneration)
	}
}

type recordingRepository struct {
	reconciliations []workloadstore.Reconciliation
	deactivated     []workloadstore.ParentKey
}

func (r *recordingRepository) Reconcile(_ context.Context, reconciliation workloadstore.Reconciliation) error {
	r.reconciliations = append(r.reconciliations, reconciliation)
	return nil
}

func (r *recordingRepository) DeactivateParent(_ context.Context, parent workloadstore.ParentKey) error {
	r.deactivated = append(r.deactivated, parent)
	return nil
}
