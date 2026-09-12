// Package kube contains narrow Kubernetes adapters used by ReplicaSense.
package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DeploymentReplicaReader returns the observed number of Deployment replicas.
// The forecaster uses this only for deterministic predictive rate limiting;
// reactive KEDA remains the source of truth for real-time scaling.
type DeploymentReplicaReader struct{ Client kubernetes.Interface }

func (r DeploymentReplicaReader) CurrentReplicas(ctx context.Context, namespace, name string) (float64, error) {
	if r.Client == nil {
		return 0, fmt.Errorf("kubernetes client is required")
	}
	deployment, err := r.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	return float64(deployment.Status.Replicas), nil
}
