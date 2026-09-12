package kube

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/canberkturan/keda-replica-sense/internal/training"
)

// TrainingJobCreator creates short-lived, cluster-local trainer Jobs. It does
// not carry metric configuration: the trainer loads the authoritative workload
// contract and canonical samples from PostgreSQL.
type TrainingJobCreator struct {
	Client             kubernetes.Interface
	Namespace          string
	Image              string
	ImagePullPolicy    corev1.PullPolicy
	ServiceAccount     string
	DatabaseSecretName string
	DatabaseSecretKey  string
	BackoffLimit       int32
	TTLSecondsAfter    int32
}

func (c TrainingJobCreator) Create(ctx context.Context, run training.Job) error {
	if c.Client == nil || c.Namespace == "" || c.Image == "" || c.DatabaseSecretName == "" || c.DatabaseSecretKey == "" {
		return fmt.Errorf("training Job creator requires client, namespace, image, and database Secret reference")
	}
	backoff := c.BackoffLimit
	if backoff < 0 {
		backoff = 0
	}
	ttl := c.TTLSecondsAfter
	if ttl <= 0 {
		ttl = 3600
	}
	pullPolicy := c.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}
	if pullPolicy != corev1.PullAlways && pullPolicy != corev1.PullIfNotPresent && pullPolicy != corev1.PullNever {
		return fmt.Errorf("training Job creator has invalid image pull policy %q", pullPolicy)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "replicasense-trainer-", Namespace: c.Namespace, Labels: map[string]string{"app": "replicasense-trainer", "replicasense.keda.sh/training-run": run.RunID}},
		Spec: batchv1.JobSpec{BackoffLimit: &backoff, TTLSecondsAfterFinished: &ttl, Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "replicasense-trainer"}},
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: c.ServiceAccount, AutomountServiceAccountToken: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "trainer", Image: c.Image, ImagePullPolicy: pullPolicy, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Env: []corev1.EnvVar{
				{Name: "REPLICASENSE_DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: c.DatabaseSecretName}, Key: c.DatabaseSecretKey}}}, {Name: "REPLICASENSE_CLUSTER_ID", Value: run.ClusterID}, {Name: "REPLICASENSE_WORKLOAD_ID", Value: run.WorkloadID}, {Name: "REPLICASENSE_TRAINING_RUN_ID", Value: run.RunID}, {Name: "REPLICASENSE_MODEL_ENGINE", Value: run.Engine},
			}}},
			}},
		},
	}
	if _, err := c.Client.BatchV1().Jobs(c.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create trainer Job: %w", err)
	}
	return nil
}
