package kube

import (
	"context"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CapacityGuard limits speculative replicas to a configurable fraction of
// currently unused allocatable CPU and memory. It is deliberately conservative:
// API errors or absent requests deny speculative scale-up.
type CapacityGuard struct {
	Client           kubernetes.Interface
	HeadroomFraction float64
}

func (g CapacityGuard) Allows(ctx context.Context, namespace, deployment string, current, desired float64) bool {
	extra := int(math.Ceil(desired - current))
	if extra <= 0 {
		return true
	}
	if g.Client == nil {
		return false
	}
	fraction := g.HeadroomFraction
	if fraction <= 0 || fraction > 1 {
		fraction = 0.25
	}
	target, err := g.Client.AppsV1().Deployments(namespace).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return false
	}
	perReplica := resources{}
	perReplica.addRequests(target.Spec.Template.Spec)
	cpu, memory := perReplica.cpu(), perReplica.memory()
	if cpu.IsZero() || memory.IsZero() {
		return false
	}
	nodes, err := g.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}
	pods, err := g.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	allocatable := resources{}
	for _, node := range nodes.Items {
		allocatable.add(node.Status.Allocatable)
	}
	requested := resources{}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			requested.addRequests(pod.Spec)
		}
	}
	extraRequest := perReplica.multiply(int64(extra))
	return fitsHeadroom(extraRequest.cpu(), allocatable.cpu(), requested.cpu(), fraction) && fitsHeadroom(extraRequest.memory(), allocatable.memory(), requested.memory(), fraction)
}

type resources struct{ cpuValue, memoryValue resource.Quantity }

func (r *resources) add(list corev1.ResourceList) {
	r.cpuValue.Add(list[corev1.ResourceCPU])
	r.memoryValue.Add(list[corev1.ResourceMemory])
}
func (r *resources) addRequests(spec corev1.PodSpec) {
	for _, container := range spec.Containers {
		r.add(container.Resources.Requests)
	}
}
func (r resources) multiply(count int64) resources {
	r.cpuValue.Mul(count)
	r.memoryValue.Mul(count)
	return r
}
func (r resources) cpu() resource.Quantity    { return r.cpuValue }
func (r resources) memory() resource.Quantity { return r.memoryValue }
func fitsHeadroom(extra, allocatable, requested resource.Quantity, fraction float64) bool {
	available := allocatable.DeepCopy()
	available.Sub(requested)
	extraScaled := extra.DeepCopy()
	extraScaled.Mul(1000)
	available.Mul(int64(math.Round(fraction * 1000)))
	return extraScaled.Cmp(available) <= 0
}
