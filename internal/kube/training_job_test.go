package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/canberkturan/keda-replica-sense/internal/training"
)

func TestTrainingJobCreatorUsesRunIdentity(t *testing.T) {
	client := fake.NewClientset()
	creator := TrainingJobCreator{Client: client, Namespace: "replicasense-system", Image: "replicasense-trainer:dev", ServiceAccount: "replicasense-trainer", DatabaseSecretName: "replicasense-database", DatabaseSecretKey: "url"}
	if err := creator.Create(context.Background(), training.Job{RunID: "run-1", ClusterID: "lab", WorkloadID: "workload-1", Engine: "seasonal-baseline", PolicyRevision: "revision-1"}); err != nil {
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
	if got := jobs.Items[0].Spec.Template.Spec.Containers[0].Env[5].Value; got != "revision-1" {
		t.Fatalf("trainer policy revision = %q, want revision-1", got)
	}
	podSecurity := jobs.Items[0].Spec.Template.Spec.SecurityContext
	containerSecurity := jobs.Items[0].Spec.Template.Spec.Containers[0].SecurityContext
	if podSecurity == nil || podSecurity.RunAsNonRoot == nil || !*podSecurity.RunAsNonRoot || containerSecurity == nil || containerSecurity.AllowPrivilegeEscalation == nil || *containerSecurity.AllowPrivilegeEscalation {
		t.Fatalf("trainer security context = pod:%#v container:%#v", podSecurity, containerSecurity)
	}
	if jobs.Items[0].Spec.Template.Spec.AutomountServiceAccountToken == nil || *jobs.Items[0].Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("trainer Job should not mount a Kubernetes API token")
	}
	if jobs.Items[0].Name != "replicasense-trainer-run-1" {
		t.Fatalf("Job name = %q", jobs.Items[0].Name)
	}
	if err := creator.Create(context.Background(), training.Job{RunID: "run-1", ClusterID: "lab", WorkloadID: "workload-1", Engine: "seasonal-baseline", PolicyRevision: "revision-1"}); err != nil {
		t.Fatalf("idempotent Job create: %v", err)
	}
}

func TestTrainingJobCreatorAppliesResourceAndExecutionBounds(t *testing.T) {
	client := fake.NewClientset()
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}
	creator := TrainingJobCreator{Client: client, Namespace: "replicasense-system", Image: "replicasense-trainer:dev", DatabaseSecretName: "replicasense-database", DatabaseSecretKey: "url", Resources: resources, ActiveDeadlineSecs: 120}
	if err := creator.Create(context.Background(), training.Job{RunID: "run-1", ClusterID: "lab", WorkloadID: "workload-1", Engine: "xgboost", PolicyRevision: "revision-1"}); err != nil {
		t.Fatal(err)
	}
	job, err := client.BatchV1().Jobs("replicasense-system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := job.Items[0].Spec.ActiveDeadlineSeconds; got == nil || *got != 120 {
		t.Fatalf("active deadline = %v, want 120", got)
	}
	if got := job.Items[0].Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("trainer CPU request = %q, want 500m", got)
	}
}

func TestTrainingJobCreatorUsesEngineSpecificImageAndWritableTmp(t *testing.T) {
	client := fake.NewSimpleClientset()
	creator := TrainingJobCreator{Client: client, Namespace: "system", Image: "trainer:go", EngineImages: map[string]string{"gru": "trainer:pytorch-gru"}, DatabaseSecretName: "database", DatabaseSecretKey: "url"}
	if err := creator.Create(context.Background(), training.Job{RunID: "run-gru", ClusterID: "lab", WorkloadID: "workload-1", Engine: "gru", PolicyRevision: "revision-1"}); err != nil {
		t.Fatal(err)
	}
	job, err := client.BatchV1().Jobs("system").Get(context.Background(), trainingJobName("run-gru"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "trainer:pytorch-gru" {
		t.Fatalf("trainer image = %q", container.Image)
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].MountPath != "/tmp" || len(job.Spec.Template.Spec.Volumes) != 1 || job.Spec.Template.Spec.Volumes[0].EmptyDir == nil {
		t.Fatalf("trainer must have an EmptyDir /tmp: %#v", job.Spec.Template.Spec)
	}
}
