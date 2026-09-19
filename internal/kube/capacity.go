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
	// Observer receives a successful cluster-capacity observation. It is kept
	// outside the decision path so monitoring can never make scaling unsafe.
	Observer func(CapacitySnapshot)
}

// CapacitySnapshot is the resource accounting result used by CapacityGuard.
// CPU values are cores and memory values are bytes when exported to metrics.
type CapacitySnapshot struct {
	Allocatable resources
	Requested   resources
	Available   resources
	Budget      resources
}

// CPU and memory accessors intentionally expose base units only: CPU cores and
// bytes. This makes the Prometheus metric unit unambiguous.
func (s CapacitySnapshot) AllocatableCPU() float64 { return quantityFloat(s.Allocatable.cpu()) }
func (s CapacitySnapshot) AllocatableMemory() float64 {
	return quantityFloat(s.Allocatable.memory())
}
func (s CapacitySnapshot) RequestedCPU() float64 { return quantityFloat(s.Requested.cpu()) }
func (s CapacitySnapshot) RequestedMemory() float64 {
	return quantityFloat(s.Requested.memory())
}
func (s CapacitySnapshot) AvailableCPU() float64 { return quantityFloat(s.Available.cpu()) }
func (s CapacitySnapshot) AvailableMemory() float64 {
	return quantityFloat(s.Available.memory())
}
func (s CapacitySnapshot) BudgetCPU() float64    { return quantityFloat(s.Budget.cpu()) }
func (s CapacitySnapshot) BudgetMemory() float64 { return quantityFloat(s.Budget.memory()) }

func quantityFloat(value resource.Quantity) float64 { return value.AsApproximateFloat64() }

func (g CapacityGuard) Allows(ctx context.Context, namespace, deployment string, current, desired float64) (allowed bool) {
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
	perReplica.addPodRequests(target.Spec.Template.Spec)
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
			requested.addPodRequests(pod.Spec)
		}
	}
	extraRequest := perReplica.multiply(int64(extra))
	available := allocatable.subtract(requested)
	budget := available.multiply(int64(math.Round(fraction * 1000))).divide(1000)
	if g.Observer != nil {
		g.Observer(CapacitySnapshot{Allocatable: allocatable, Requested: requested, Available: available, Budget: budget})
	}
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
func (r *resources) addPodRequests(spec corev1.PodSpec) {
	regular := resources{}
	regular.addRequests(spec)
	initialization := resources{}
	for _, container := range spec.InitContainers {
		candidate := resources{}
		candidate.add(container.Resources.Requests)
		initialization.max(candidate)
	}
	// Kubernetes schedules a pod using the larger of its summed regular
	// container requests and its largest init-container request; it does not
	// reserve both at once. Pod overhead is then added to that effective value.
	regular.max(initialization)
	regular.add(spec.Overhead)
	r.add(regular.resourceList())
}
func (r *resources) max(other resources) {
	if other.cpuValue.Cmp(r.cpuValue) > 0 {
		r.cpuValue = other.cpuValue.DeepCopy()
	}
	if other.memoryValue.Cmp(r.memoryValue) > 0 {
		r.memoryValue = other.memoryValue.DeepCopy()
	}
}
func (r resources) subtract(other resources) resources {
	r.cpuValue.Sub(other.cpuValue)
	r.memoryValue.Sub(other.memoryValue)
	return r
}
func (r resources) divide(divisor int64) resources {
	if divisor > 1 {
		r.cpuValue.SetMilli(r.cpuValue.MilliValue() / divisor)
		r.memoryValue.Set(r.memoryValue.Value() / divisor)
	}
	return r
}
func (r resources) resourceList() corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: r.cpuValue, corev1.ResourceMemory: r.memoryValue}
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
