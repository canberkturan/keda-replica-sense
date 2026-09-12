package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClusterHealthChecker(t *testing.T) {
	readyNode := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	checker := ClusterHealthChecker{Client: fake.NewClientset(readyNode)}
	if !checker.Healthy(context.Background()) {
		t.Fatal("Healthy() = false, want true")
	}
}

func TestClusterHealthCheckerRejectsUnreadyOrUnschedulable(t *testing.T) {
	unready := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	if (ClusterHealthChecker{Client: fake.NewClientset(unready)}).Healthy(context.Background()) {
		t.Fatal("Healthy() = true with unready node")
	}
	ready := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending"}, Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}}}}
	if (ClusterHealthChecker{Client: fake.NewClientset(ready, pending)}).Healthy(context.Background()) {
		t.Fatal("Healthy() = true with unschedulable pod")
	}
}
