package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClusterHealthChecker applies the minimum health gate before speculative
// scale-up. It deliberately fails closed when Kubernetes cannot be queried.
type ClusterHealthChecker struct {
	Client               kubernetes.Interface
	MaxUnschedulablePods int
}

func (c ClusterHealthChecker) Healthy(ctx context.Context) bool {
	if c.Client == nil {
		return false
	}
	nodes, err := c.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}
	for _, node := range nodes.Items {
		if !nodeReady(node) {
			return false
		}
	}
	pods, err := c.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	unschedulable := 0
	for _, pod := range pods.Items {
		if podUnschedulable(pod) {
			unschedulable++
		}
	}
	return unschedulable <= c.MaxUnschedulablePods
}

func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podUnschedulable(pod corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodPending {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
			return true
		}
	}
	return false
}
