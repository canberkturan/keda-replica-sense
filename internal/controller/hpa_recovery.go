package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
	"github.com/canberkturan/keda-replica-sense/internal/scaler"
)

const HPARecoveryAnnotation = "replicasense.keda.sh/predictive-metric-recovery"

// PredictiveMetricProbe checks the entire KEDA metrics path. A zero value is
// healthy: restoring metric discovery must not depend on a trained model.
type PredictiveMetricProbe interface {
	Check(context.Context, string, string, string) error
}

type HPARecoveryObserver interface {
	RecordMissing(clusterID, namespace, scaledObject string, count int)
	RecordOutcome(string)
	Delete(clusterID, namespace, scaledObject string)
}

type hpaRecoveryState struct {
	uid         types.UID
	attempts    int
	nextAttempt time.Time
}

// HPARecovery repairs discovery after KEDA starts before the external scaler.
// It requests reconciliation through HPA metadata, which KEDA watches, rather
// than changing ScaledObject policy, HPA metric specs, or replica counts.
// State is owned by the single leader-elected runnable; no other goroutine
// should call Check concurrently.
type HPARecovery struct {
	Reader        client.Reader
	Writer        client.Writer
	ParserOptions scaledobject.ParserOptions
	Probe         PredictiveMetricProbe
	Observer      HPARecoveryObserver
	Interval      time.Duration
	ProbeTimeout  time.Duration
	Log           logr.Logger
	states        map[types.NamespacedName]hpaRecoveryState
}

func (*HPARecovery) NeedLeaderElection() bool { return true }

func (r *HPARecovery) Start(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		if _, err := r.Check(ctx, time.Now()); err != nil && ctx.Err() == nil {
			r.Log.Error(err, "check predictive HPA metric recovery")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check scans existing HPAs only. KEDA remains responsible for HPA creation.
// Failed probes and repair requests use exponential backoff, capped at five
// minutes (or the configured scan interval when it is longer).
func (r *HPARecovery) Check(ctx context.Context, now time.Time) (int, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	var objects kedav1alpha1.ScaledObjectList
	if err := r.Reader.List(ctx, &objects); err != nil {
		return 0, fmt.Errorf("list ScaledObjects for HPA recovery: %w", err)
	}
	if r.states == nil {
		r.states = make(map[types.NamespacedName]hpaRecoveryState)
	}
	seen := make(map[types.NamespacedName]bool)
	requested := 0
	var failures []error
	for index := range objects.Items {
		object := &objects.Items[index]
		metrics := r.expectedMetrics(object)
		if len(metrics) == 0 {
			continue
		}
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: object.Namespace, Name: recoveryHPAName(object)}, &hpa); err != nil {
			if !apierrors.IsNotFound(err) {
				failures = append(failures, fmt.Errorf("read HPA for %s/%s: %w", object.Namespace, object.Name, err))
			}
			continue
		}
		if !ownedRecoveryHPA(&hpa, object) {
			continue
		}
		key := client.ObjectKeyFromObject(object)
		seen[key] = true
		missing := missingHPAMetrics(&hpa, metrics)
		if r.Observer != nil {
			r.Observer.RecordMissing(r.ParserOptions.ClusterID, key.Namespace, key.Name, len(missing))
		}
		state := r.states[key]
		if state.uid != hpa.UID {
			state = hpaRecoveryState{uid: hpa.UID}
		}
		if len(missing) == 0 {
			if state.attempts > 0 {
				r.outcome("restored")
				r.Log.Info("predictive HPA metrics restored", "namespace", key.Namespace, "scaledObject", key.Name, "hpa", hpa.Name)
			}
			r.states[key] = hpaRecoveryState{uid: hpa.UID}
			continue
		}
		if now.Before(state.nextAttempt) {
			continue
		}
		if state.attempts < 10 {
			state.attempts++
		}
		state.nextAttempt = now.Add(r.backoff(state.attempts))
		r.states[key] = state
		if err := r.probe(ctx, object, missing); err != nil {
			r.outcome("probe_failed")
			failures = append(failures, fmt.Errorf("probe predictive metric for %s/%s: %w", key.Namespace, key.Name, err))
			continue
		}
		before := hpa.DeepCopy()
		if hpa.Annotations == nil {
			hpa.Annotations = make(map[string]string)
		}
		hpa.Annotations[HPARecoveryAnnotation] = now.UTC().Format(time.RFC3339Nano)
		// The resource-version precondition prevents patching a replaced HPA or
		// racing KEDA's own update. Retry from a new observation after conflicts.
		if err := r.Writer.Patch(ctx, &hpa, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			r.outcome("patch_failed")
			failures = append(failures, fmt.Errorf("request HPA recovery for %s/%s: %w", key.Namespace, key.Name, err))
			continue
		}
		requested++
		r.outcome("requested")
		r.Log.Info("requested KEDA predictive metric reconciliation", "namespace", key.Namespace, "scaledObject", key.Name, "hpa", hpa.Name, "missingMetrics", missing)
	}
	for key := range r.states {
		if !seen[key] {
			delete(r.states, key)
			if r.Observer != nil {
				r.Observer.Delete(r.ParserOptions.ClusterID, key.Namespace, key.Name)
			}
		}
	}
	return requested, errors.Join(failures...)
}

func (r *HPARecovery) validate() error {
	if r.Reader == nil || r.Writer == nil || r.Probe == nil || r.ParserOptions.ClusterID == "" || len(r.ParserOptions.ScalerAddresses) == 0 {
		return fmt.Errorf("HPA recovery requires Kubernetes reader, writer, metric probe, and parser identity")
	}
	return nil
}

func (r *HPARecovery) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return 30 * time.Second
}

func (r *HPARecovery) backoff(attempt int) time.Duration {
	delay, maximum := r.interval(), 5*time.Minute
	if maximum < delay {
		maximum = delay
	}
	for i := 1; i < attempt && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return delay
}

func (r *HPARecovery) outcome(outcome string) {
	if r.Observer != nil {
		r.Observer.RecordOutcome(outcome)
	}
}

func (r *HPARecovery) probe(ctx context.Context, object *kedav1alpha1.ScaledObject, missing []string) error {
	timeout := r.ProbeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, metric := range missing {
		if err := r.Probe.Check(ctx, object.Namespace, object.Name, metric); err != nil {
			return err
		}
	}
	return nil
}

func (r *HPARecovery) expectedMetrics(object *kedav1alpha1.ScaledObject) []string {
	// A scalingModifiers HPA intentionally uses a composite metric instead of
	// individual trigger metrics. Never mistake that topology for an outage.
	if !object.DeletionTimestamp.IsZero() || object.HasPausedAnnotation() || object.IsUsingModifiers() {
		return nil
	}
	input, err := scaledobject.InputFromKEDA(object)
	if err != nil {
		return nil
	}
	result := scaledobject.Parse(input, r.ParserOptions)
	var metrics []string
	for _, candidate := range result.Candidates {
		if candidate.Spec == nil {
			continue
		}
		for index, trigger := range object.Spec.Triggers {
			if trigger.Name == candidate.PredictiveTriggerName {
				metrics = append(metrics, fmt.Sprintf("s%d-%s", index, scaler.MetricName))
				break
			}
		}
	}
	return metrics
}

func recoveryHPAName(object *kedav1alpha1.ScaledObject) string {
	if advanced := object.Spec.Advanced; advanced != nil && advanced.HorizontalPodAutoscalerConfig != nil && advanced.HorizontalPodAutoscalerConfig.Name != "" {
		return advanced.HorizontalPodAutoscalerConfig.Name
	}
	if object.Status.HpaName != "" {
		return object.Status.HpaName
	}
	return "keda-hpa-" + object.Name
}

func ownedRecoveryHPA(hpa *autoscalingv2.HorizontalPodAutoscaler, object *kedav1alpha1.ScaledObject) bool {
	owner := metav1.GetControllerOf(hpa)
	return hpa.DeletionTimestamp.IsZero() && object.UID != "" && owner != nil && owner.APIVersion == kedav1alpha1.SchemeGroupVersion.String() && owner.Kind == "ScaledObject" && owner.Name == object.Name && owner.UID == object.UID
}

func missingHPAMetrics(hpa *autoscalingv2.HorizontalPodAutoscaler, expected []string) []string {
	present := make(map[string]bool)
	for _, metric := range hpa.Spec.Metrics {
		if metric.Type == autoscalingv2.ExternalMetricSourceType && metric.External != nil {
			present[metric.External.Metric.Name] = true
		}
	}
	var missing []string
	for _, name := range expected {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
