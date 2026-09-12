package kube

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDeploymentReplicaReaderReadsObservedReplicaCount(t *testing.T) {
	client := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "checkout"}, Status: appsv1.DeploymentStatus{Replicas: 4}})
	got, err := (DeploymentReplicaReader{Client: client}).CurrentReplicas(context.Background(), "payments", "checkout")
	if err != nil || got != 4 {
		t.Fatalf("CurrentReplicas() = %v, %v; want 4, nil", got, err)
	}
}
