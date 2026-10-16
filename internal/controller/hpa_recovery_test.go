package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
)

type recordingMetricProbe struct {
	err   error
	calls []string
}

func (p *recordingMetricProbe) Check(ctx context.Context, namespace, object, metric string) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("probe must have a deadline")
	}
	p.calls = append(p.calls, namespace+"/"+object+"/"+metric)
	return p.err
}

type recordingRecoveryObserver struct {
	missing  map[string]int
	outcomes map[string]int
}

func (o *recordingRecoveryObserver) RecordMissing(_, namespace, object string, count int) {
	o.missing[namespace+"/"+object] = count
}

func (o *recordingRecoveryObserver) RecordOutcome(outcome string) { o.outcomes[outcome]++ }
func (o *recordingRecoveryObserver) Delete(_, namespace, object string) {
	delete(o.missing, namespace+"/"+object)
}

func recoveryFixture(t *testing.T, object *kedav1alpha1.ScaledObject, hpa *autoscalingv2.HorizontalPodAutoscaler) (*HPARecovery, client.Client, *recordingMetricProbe, *recordingRecoveryObserver) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kedav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := autoscalingv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{object}
	if hpa != nil {
		objects = append(objects, hpa)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	probe := &recordingMetricProbe{}
	observer := &recordingRecoveryObserver{missing: map[string]int{}, outcomes: map[string]int{}}
	return &HPARecovery{
		Reader: c, Writer: c, Probe: probe, Observer: observer, Log: logr.Discard(),
		ParserOptions: scaledobject.ParserOptions{ClusterID: "cluster-a", ScalerAddresses: map[string]struct{}{"replicasense.default.svc:6000": {}}, DefaultMaxReplicaCount: 100},
	}, c, probe, observer
}

func recoveryTestHPA(object *kedav1alpha1.ScaledObject) *autoscalingv2.HorizontalPodAutoscaler {
	controller := true
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: object.Namespace, Name: "keda-hpa-" + object.Name, UID: "hpa-uid", ResourceVersion: "100",
			Annotations:     map[string]string{"keep": "annotation"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: kedav1alpha1.SchemeGroupVersion.String(), Kind: "ScaledObject", Name: object.Name, UID: object.UID, Controller: &controller}},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "payments-api"},
			MaxReplicas:    50,
			Metrics:        []autoscalingv2.MetricSpec{recoveryTestMetric("s0-prometheus")},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3, DesiredReplicas: 3},
	}
}

func recoveryTestMetric(name string) autoscalingv2.MetricSpec {
	return autoscalingv2.MetricSpec{Type: autoscalingv2.ExternalMetricSourceType, External: &autoscalingv2.ExternalMetricSource{Metric: autoscalingv2.MetricIdentifier{Name: name}}}
}

func TestHPARecoveryRequestsOnlyMetadataReconciliation(t *testing.T) {
	object := predictiveScaledObject()
	object.Spec.Triggers[1].Metadata["clearOldModels"] = "true"
	object.Spec.Triggers[1].Metadata["immediateTraining"] = "true"
	hpa := recoveryTestHPA(object)
	r, c, probe, observer := recoveryFixture(t, object, hpa)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	count, err := r.Check(context.Background(), now)
	if err != nil || count != 1 {
		t.Fatalf("Check() = %d, %v; want one request", count, err)
	}
	var gotHPA autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(hpa), &gotHPA); err != nil {
		t.Fatal(err)
	}
	if gotHPA.Annotations[HPARecoveryAnnotation] != now.Format(time.RFC3339Nano) || gotHPA.Annotations["keep"] != "annotation" {
		t.Fatalf("wrong annotations: %v", gotHPA.Annotations)
	}
	if !reflect.DeepEqual(gotHPA.Spec, hpa.Spec) || !reflect.DeepEqual(gotHPA.Status, hpa.Status) {
		t.Fatal("recovery must not modify HPA specification or replica decisions")
	}
	var gotObject kedav1alpha1.ScaledObject
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(object), &gotObject); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotObject.Spec, object.Spec) {
		t.Fatal("recovery must not change the policy or training settings")
	}
	if len(probe.calls) != 1 || probe.calls[0] != "default/payments-api/s1-replicasense_predictive" || observer.outcomes["requested"] != 1 || observer.missing["default/payments-api"] != 1 {
		t.Fatalf("probe/metrics = %v, %#v", probe.calls, observer)
	}
}

func TestHPARecoverySkipsUnmanagedHealthyPausedAndCompositeObjects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*kedav1alpha1.ScaledObject, *autoscalingv2.HorizontalPodAutoscaler)
	}{
		{"healthy", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Spec.Metrics = append(h.Spec.Metrics, recoveryTestMetric("s1-replicasense_predictive"))
		}},
		{"other scaler", func(s *kedav1alpha1.ScaledObject, _ *autoscalingv2.HorizontalPodAutoscaler) {
			s.Spec.Triggers[1].Metadata["scalerAddress"] = "other.default.svc:6000"
		}},
		{"invalid policy", func(s *kedav1alpha1.ScaledObject, _ *autoscalingv2.HorizontalPodAutoscaler) {
			s.Spec.Triggers[1].Metadata["quantile"] = "2"
		}},
		{"not owned", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) { h.OwnerReferences = nil }},
		{"stale owner UID", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			h.OwnerReferences[0].UID = "old-scaledobject"
		}},
		{"wrong owner group", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			h.OwnerReferences[0].APIVersion = "other/v1"
		}},
		{"non-controller owner", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			*h.OwnerReferences[0].Controller = false
		}},
		{"paused", func(s *kedav1alpha1.ScaledObject, _ *autoscalingv2.HorizontalPodAutoscaler) {
			s.Annotations = map[string]string{kedav1alpha1.PausedAnnotation: "true"}
		}},
		{"paused replicas", func(s *kedav1alpha1.ScaledObject, _ *autoscalingv2.HorizontalPodAutoscaler) {
			s.Annotations = map[string]string{kedav1alpha1.PausedReplicasAnnotation: "3"}
		}},
		{"composite", func(s *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			s.Spec.Advanced = &kedav1alpha1.AdvancedConfig{ScalingModifiers: kedav1alpha1.ScalingModifiers{Formula: "max(reactive,predictive)", Target: "1"}}
			h.Spec.Metrics = []autoscalingv2.MetricSpec{recoveryTestMetric("composite-metric")}
		}},
		{"deleting HPA", func(_ *kedav1alpha1.ScaledObject, h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Finalizers = []string{"test"}
			h.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		}},
		{"deleting ScaledObject", func(s *kedav1alpha1.ScaledObject, _ *autoscalingv2.HorizontalPodAutoscaler) {
			s.Finalizers = []string{"test"}
			s.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := predictiveScaledObject()
			hpa := recoveryTestHPA(object)
			test.mutate(object, hpa)
			r, c, probe, _ := recoveryFixture(t, object, hpa)
			count, err := r.Check(context.Background(), time.Now())
			if err != nil || count != 0 || len(probe.calls) != 0 {
				t.Fatalf("Check() = %d, %v; probe calls = %v", count, err, probe.calls)
			}
			var got autoscalingv2.HorizontalPodAutoscaler
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(hpa), &got); err != nil {
				t.Fatal(err)
			}
			if got.Annotations[HPARecoveryAnnotation] != "" {
				t.Fatal("unexpected recovery annotation")
			}
		})
	}
}

func TestHPARecoveryDoesNotCreateMissingHPA(t *testing.T) {
	r, _, probe, _ := recoveryFixture(t, predictiveScaledObject(), nil)
	if count, err := r.Check(context.Background(), time.Now()); err != nil || count != 0 || len(probe.calls) != 0 {
		t.Fatalf("Check() = %d, %v; want no creation", count, err)
	}
}

func TestHPARecoveryBackoffAndResetAfterRestoration(t *testing.T) {
	object := predictiveScaledObject()
	hpa := recoveryTestHPA(object)
	r, c, probe, observer := recoveryFixture(t, object, hpa)
	now := time.Now()
	probe.err = errors.New("scaler unavailable")
	if count, err := r.Check(context.Background(), now); count != 0 || err == nil {
		t.Fatalf("unhealthy Check() = %d, %v", count, err)
	}
	if _, err := r.Check(context.Background(), now.Add(29*time.Second)); err != nil || len(probe.calls) != 1 {
		t.Fatalf("probe must respect backoff: calls=%v, err=%v", probe.calls, err)
	}
	probe.err = nil
	if count, err := r.Check(context.Background(), now.Add(30*time.Second)); count != 1 || err != nil {
		t.Fatalf("recovered probe Check() = %d, %v", count, err)
	}
	if count, err := r.Check(context.Background(), now.Add(89*time.Second)); count != 0 || err != nil || len(probe.calls) != 2 {
		t.Fatalf("repair requests must also back off: %d, %v, %v", count, err, probe.calls)
	}
	var current autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(hpa), &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Metrics = append(current.Spec.Metrics, recoveryTestMetric("s1-replicasense_predictive"))
	if err := c.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if count, err := r.Check(context.Background(), now.Add(90*time.Second)); count != 0 || err != nil || observer.outcomes["restored"] != 1 || observer.missing["default/payments-api"] != 0 {
		t.Fatalf("restoration Check() = %d, %v, %#v", count, err, observer)
	}
	current.Spec.Metrics = current.Spec.Metrics[:1]
	if err := c.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if count, err := r.Check(context.Background(), now.Add(91*time.Second)); count != 1 || err != nil {
		t.Fatalf("new outage should start a fresh recovery: %d, %v", count, err)
	}
	if delay := r.backoff(100); delay != 5*time.Minute {
		t.Fatalf("maximum backoff = %v", delay)
	}
}

func TestHPARecoveryUsesCustomHPAAndOriginalTriggerIndices(t *testing.T) {
	object := predictiveScaledObject()
	object.Spec.Advanced = &kedav1alpha1.AdvancedConfig{HorizontalPodAutoscalerConfig: &kedav1alpha1.HorizontalPodAutoscalerConfig{Name: "custom-hpa"}}
	object.Status.HpaName = "stale-hpa-name"
	object.Spec.Triggers = append(object.Spec.Triggers,
		kedav1alpha1.ScaleTriggers{Type: "prometheus", Name: "reactive-two", Metadata: map[string]string{"serverAddress": "http://prometheus:9090", "query": "up", "threshold": "100"}},
		kedav1alpha1.ScaleTriggers{Type: "external", Name: "predictive-two", Metadata: map[string]string{"scalerAddress": "replicasense.default.svc:6000", "sourceTrigger": "reactive-two"}},
	)
	hpa := recoveryTestHPA(object)
	hpa.Name = "custom-hpa"
	hpa.Spec.Metrics = append(hpa.Spec.Metrics, recoveryTestMetric("s1-replicasense_predictive"))
	r, _, probe, _ := recoveryFixture(t, object, hpa)
	if count, err := r.Check(context.Background(), time.Now()); count != 1 || err != nil {
		t.Fatalf("Check() = %d, %v", count, err)
	}
	if !reflect.DeepEqual(probe.calls, []string{"default/payments-api/s3-replicasense_predictive"}) {
		t.Fatalf("wrong metric indices: %v", probe.calls)
	}
}

type conflictingRecoveryWriter struct{ client.Client }

func (w conflictingRecoveryWriter) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return apierrors.NewConflict(schema.GroupResource{Group: "autoscaling", Resource: "horizontalpodautoscalers"}, "hpa", errors.New("changed concurrently"))
}

func TestHPARecoveryConflictsRetryWithoutOverwritingHPA(t *testing.T) {
	object := predictiveScaledObject()
	hpa := recoveryTestHPA(object)
	r, c, probe, observer := recoveryFixture(t, object, hpa)
	r.Writer = conflictingRecoveryWriter{Client: c}
	now := time.Now()
	if count, err := r.Check(context.Background(), now); count != 0 || !apierrors.IsConflict(err) || observer.outcomes["patch_failed"] != 1 {
		t.Fatalf("conflict Check() = %d, %v, %#v", count, err, observer)
	}
	r.Writer = c
	if count, err := r.Check(context.Background(), now.Add(time.Second)); count != 0 || err != nil || len(probe.calls) != 1 {
		t.Fatalf("conflict retry must respect backoff: %d, %v", count, err)
	}
	if count, err := r.Check(context.Background(), now.Add(30*time.Second)); count != 1 || err != nil {
		t.Fatalf("retry Check() = %d, %v", count, err)
	}
}

func TestHPARecoveryPrunesDeletedObjectsAndMetrics(t *testing.T) {
	object := predictiveScaledObject()
	r, c, _, observer := recoveryFixture(t, object, recoveryTestHPA(object))
	if _, err := r.Check(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	if count, err := r.Check(context.Background(), time.Now()); count != 0 || err != nil || len(r.states) != 0 || len(observer.missing) != 0 {
		t.Fatalf("deleted-object state must be pruned: %d, %v, %v, %v", count, err, r.states, observer.missing)
	}
}

func TestHPARecoveryRequiresLeaderElectionAndConfiguration(t *testing.T) {
	r := &HPARecovery{}
	if !r.NeedLeaderElection() {
		t.Fatal("only the elected controller should repair HPAs")
	}
	if _, err := r.Check(context.Background(), time.Now()); err == nil {
		t.Fatal("missing configuration must fail")
	}
	if r.interval() != 30*time.Second {
		t.Fatal("default recovery scan must be 30s")
	}
}
