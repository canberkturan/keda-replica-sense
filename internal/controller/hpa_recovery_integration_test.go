package controller

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/canberkturan/keda-replica-sense/internal/kube"
	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
)

// This opt-in test uses a disposable zero-replica workload and an initially
// empty Service alias, never interrupting the real scaler or benchmark. It
// reproduces KEDA's partial HPA discovery and exercises actual API recovery.
func TestHPARecoveryKEDARestartIntegration(t *testing.T) {
	if os.Getenv("REPLICASENSE_HPA_RECOVERY_LAB_TEST") != "1" {
		t.Skip("set REPLICASENSE_HPA_RECOVERY_LAB_TEST=1 to run against keda-lab-v2")
	}
	configuration, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: "keda-lab-v2"}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kedav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(configuration, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "replicasense-hpa-recovery-test-"}}
	if err := c.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	alias := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "replicasense-system", GenerateName: "replicasense-recovery-probe-"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"replicasense.keda.sh/recovery-test": "no-endpoints"}, Ports: []corev1.ServicePort{{Name: "grpc", Port: 6000}}},
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := c.Delete(cleanupCtx, namespace); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("clean test namespace %s: %v", namespace.Name, err)
		}
		if alias.Name != "" {
			if err := c.Delete(cleanupCtx, alias); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("clean test Service %s: %v", alias.Name, err)
			}
		}
	}()
	if err := c.Create(ctx, alias); err != nil {
		t.Fatal(err)
	}
	zero, one := int32(0), int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace.Name, Name: "recovery-test"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "recovery-test"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "recovery-test"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}}},
		},
	}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	address := alias.Name + ".replicasense-system.svc:6000"
	object := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace.Name, Name: "recovery-test"},
		Spec: kedav1alpha1.ScaledObjectSpec{
			ScaleTargetRef: &kedav1alpha1.ScaleTarget{Name: deployment.Name}, MinReplicaCount: &zero, MaxReplicaCount: &one,
			Triggers: []kedav1alpha1.ScaleTriggers{
				{Type: "prometheus", Name: "reactive", Metadata: map[string]string{"serverAddress": "http://monitoring-kube-prometheus-prometheus.monitoring.svc:9090", "query": "vector(0)", "threshold": "500"}},
				{Type: "external", Name: "predictive", Metadata: map[string]string{"scalerAddress": address, "sourceTrigger": "reactive", "immediateTraining": "true", "clearOldModels": "true"}},
			},
		},
	}
	if err := c.Create(ctx, object); err != nil {
		t.Fatal(err)
	}
	hpaKey := client.ObjectKey{Namespace: namespace.Name, Name: recoveryHPAName(object)}
	if err := awaitRecoveryCondition(ctx, func() (bool, error) {
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if err := c.Get(ctx, hpaKey, &hpa); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return len(hpa.Spec.Metrics) == 1 && hpa.Spec.Metrics[0].External != nil && hpa.Spec.Metrics[0].External.Metric.Name == "s0-prometheus", nil
	}); err != nil {
		t.Fatalf("reproduce missing predictive HPA metric: %v", err)
	}
	t.Log("reproduced: KEDA created an HPA containing only the reactive metric")
	probe, err := kube.NewPredictiveMetricProbe(configuration)
	if err != nil {
		t.Fatal(err)
	}
	recovery := &HPARecovery{
		Reader: c, Writer: c, Probe: probe, Log: logr.Discard(),
		ParserOptions: scaledobject.ParserOptions{ClusterID: "keda-lab-v2", ScalerAddresses: map[string]struct{}{address: {}}, DefaultMaxReplicaCount: 100},
	}
	// Check while the alias is unavailable: no repair signal should be issued.
	if requested, err := recovery.Check(ctx, time.Now()); requested != 0 || err == nil {
		t.Fatalf("unavailable scaler must withhold repair: requested=%d err=%v", requested, err)
	}
	var realService corev1.Service
	if err := c.Get(ctx, client.ObjectKey{Namespace: "replicasense-system", Name: "replicasense-replicasense-scaler"}, &realService); err != nil {
		t.Fatal(err)
	}
	alias.Spec.Selector = realService.Spec.Selector
	if err := c.Update(ctx, alias); err != nil {
		t.Fatal(err)
	}
	t.Log("restored only the disposable Service alias; starting automatic recovery loop")
	loopCtx, stopLoop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- recovery.Start(loopCtx) }()
	defer func() {
		stopLoop()
		if err := <-done; err != nil {
			t.Errorf("recovery loop: %v", err)
		}
	}()
	if err := awaitRecoveryCondition(ctx, func() (bool, error) {
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if err := c.Get(ctx, hpaKey, &hpa); err != nil {
			return false, err
		}
		return len(missingHPAMetrics(&hpa, []string{"s0-prometheus", "s1-replicasense_predictive"})) == 0, nil
	}); err != nil {
		t.Fatalf("automatic predictive HPA recovery: %v", err)
	}
	var unchanged kedav1alpha1.ScaledObject
	if err := c.Get(ctx, client.ObjectKeyFromObject(object), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Generation != object.Generation || !reflect.DeepEqual(unchanged.Spec, object.Spec) {
		t.Fatal("recovery changed ScaledObject policy or training settings")
	}
	if err := probe.Check(ctx, object.Namespace, object.Name, "s1-replicasense_predictive"); err != nil {
		t.Fatalf("recovered predictive metric must be readable even without a trained model: %v", err)
	}
	t.Log("verified: KEDA restored both metrics automatically; ScaledObject policy and generation are unchanged")
}

func awaitRecoveryCondition(ctx context.Context, condition func() (bool, error)) error {
	for {
		ready, err := condition()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
