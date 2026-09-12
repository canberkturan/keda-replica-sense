package kube

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCapacityGuardAllowsOnlyAvailableHeadroom(t *testing.T) {
	requests := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "api"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: requests}}}}}}}
	node := &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10"), corev1.ResourceMemory: resource.MustParse("10Gi")}}}
	used := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("7"), corev1.ResourceMemory: resource.MustParse("7Gi")}}}}}}
	guard := CapacityGuard{Client: fake.NewClientset(deployment, node, used), HeadroomFraction: 1}
	if !guard.Allows(context.Background(), "apps", "api", 1, 3) {
		t.Fatal("two extra replicas should fit three units of headroom")
	}
	if guard.Allows(context.Background(), "apps", "api", 1, 5) {
		t.Fatal("four extra replicas must exceed three units of headroom")
	}
}
