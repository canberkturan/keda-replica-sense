// Package controller contains Kubernetes reconciliation orchestration.
package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
)

type ScaledObjectReference struct {
	ClusterID          string
	Namespace          string
	ScaledObjectName   string
	UID                string
	ObservedGeneration int64
}

type Observation struct {
	Source ScaledObjectReference
	Result scaledobject.ParseResult
}

// WorkloadSink is the persistence seam. This first implementation logs
// observations only; PostgreSQL will implement the same interface in M3.
type WorkloadSink interface {
	Observe(context.Context, Observation) error
	DeactivateScaledObject(context.Context, ScaledObjectReference) error
}

type Processor struct {
	ParserOptions scaledobject.ParserOptions
	Sink          WorkloadSink
}

func (p Processor) ObserveScaledObject(ctx context.Context, object *kedav1alpha1.ScaledObject) error {
	if p.Sink == nil {
		return fmt.Errorf("workload sink is required")
	}
	input, err := scaledobject.InputFromKEDA(object)
	if err != nil {
		return err
	}
	return p.Sink.Observe(ctx, Observation{
		Source: p.reference(input.Namespace, input.Name, input.UID, input.Generation),
		Result: scaledobject.Parse(input, p.ParserOptions),
	})
}

func (p Processor) DeactivateScaledObject(ctx context.Context, namespace, name string) error {
	if p.Sink == nil {
		return fmt.Errorf("workload sink is required")
	}
	return p.Sink.DeactivateScaledObject(ctx, p.reference(namespace, name, "", 0))
}

func (p Processor) reference(namespace, name, uid string, generation int64) ScaledObjectReference {
	return ScaledObjectReference{
		ClusterID:          p.ParserOptions.ClusterID,
		Namespace:          namespace,
		ScaledObjectName:   name,
		UID:                uid,
		ObservedGeneration: generation,
	}
}

// ScaledObjectReconciler fetches KEDA objects and delegates lifecycle handling
// to Processor. It never registers workloads through KEDA gRPC calls.
type ScaledObjectReconciler struct {
	client.Client
	Processor Processor
}

func (r *ScaledObjectReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var object kedav1alpha1.ScaledObject
	if err := r.Get(ctx, request.NamespacedName, &object); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Processor.DeactivateScaledObject(ctx, request.Namespace, request.Name)
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.Processor.ObserveScaledObject(ctx, &object)
}

func (r *ScaledObjectReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&kedav1alpha1.ScaledObject{}).
		Complete(r)
}

// LoggingSink provides read-only M2 behavior. It deliberately avoids logging
// PromQL and other metadata that may contain sensitive label values.
type LoggingSink struct {
	Log logr.Logger
}

func (s LoggingSink) Observe(_ context.Context, observation Observation) error {
	s.Log.Info("observed ScaledObject", "clusterID", observation.Source.ClusterID, "namespace", observation.Source.Namespace, "scaledObject", observation.Source.ScaledObjectName, "candidates", len(observation.Result.Candidates), "violations", len(observation.Result.Violations))
	for _, candidate := range observation.Result.Candidates {
		if candidate.Spec == nil {
			s.Log.Info("predictive trigger is invalid", "clusterID", observation.Source.ClusterID, "namespace", observation.Source.Namespace, "scaledObject", observation.Source.ScaledObjectName, "predictiveTrigger", candidate.PredictiveTriggerName, "violations", candidate.Violations)
			continue
		}
		s.Log.Info("predictive workload discovered", "clusterID", candidate.Spec.Key.ClusterID, "namespace", candidate.Spec.Key.Namespace, "scaledObject", candidate.Spec.Key.ScaledObjectName, "predictiveTrigger", candidate.Spec.Key.PredictiveTriggerName, "sourceFingerprint", candidate.Spec.SourceFingerprint, "policyRevision", candidate.Spec.PolicyRevision)
	}
	return nil
}

func (s LoggingSink) DeactivateScaledObject(_ context.Context, reference ScaledObjectReference) error {
	s.Log.Info("ScaledObject deleted", "clusterID", reference.ClusterID, "namespace", reference.Namespace, "scaledObject", reference.ScaledObjectName)
	return nil
}
