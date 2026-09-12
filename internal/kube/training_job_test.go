package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/canberkturan/keda-replica-sense/internal/training"
)

func TestTrainingJobCreatorUsesRunIdentity(t *testing.T) {
	client := fake.NewClientset()
	creator := TrainingJobCreator{Client: client, Namespace: "replicasense-system", Image: "replicasense-trainer:dev", ServiceAccount: "replicasense-trainer", DatabaseSecretName: "replicasense-database", DatabaseSecretKey: "url"}
	if err := creator.Create(context.Background(), training.Job{RunID: "run-1", ClusterID: "lab", WorkloadID: "workload-1", Engine: "seasonal-baseline"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := client.BatchV1().Jobs("replicasense-system").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("Jobs() = %#v, %v", jobs, err)
	}
	if jobs.Items[0].Spec.Template.Spec.Containers[0].Env[0].ValueFrom == nil || jobs.Items[0].Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name != "replicasense-database" {
		t.Fatalf("trainer database environment must reference a Secret: %#v", jobs.Items[0].Spec.Template.Spec.Containers[0].Env[0])
	}
	if jobs.Items[0].Spec.Template.Spec.Containers[0].Env[2].Value != "workload-1" {
		t.Fatalf("trainer environment = %#v", jobs.Items[0].Spec.Template.Spec.Containers[0].Env)
	}
	podSecurity := jobs.Items[0].Spec.Template.Spec.SecurityContext
	containerSecurity := jobs.Items[0].Spec.Template.Spec.Containers[0].SecurityContext
	if podSecurity == nil || podSecurity.RunAsNonRoot == nil || !*podSecurity.RunAsNonRoot || containerSecurity == nil || containerSecurity.AllowPrivilegeEscalation == nil || *containerSecurity.AllowPrivilegeEscalation {
		t.Fatalf("trainer security context = pod:%#v container:%#v", podSecurity, containerSecurity)
	}
	if jobs.Items[0].Spec.Template.Spec.AutomountServiceAccountToken == nil || *jobs.Items[0].Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("trainer Job should not mount a Kubernetes API token")
	}
}
