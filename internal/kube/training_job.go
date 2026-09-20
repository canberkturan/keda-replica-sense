package kube

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/canberkturan/keda-replica-sense/internal/training"
)

// TrainingJobCreator creates short-lived, cluster-local trainer Jobs. It does
// not carry metric configuration: the trainer loads the authoritative workload
// contract and canonical samples from PostgreSQL.
type TrainingJobCreator struct {
	Client    kubernetes.Interface
	Namespace string
	Image     string
	// EngineImages overrides Image for a trained engine. It allows a short-lived
	// CPU PyTorch Job to train GRU artifacts while native Go/XGBoost jobs keep
	// their existing image and dependency boundary.
	EngineImages       map[string]string
	ImagePullPolicy    corev1.PullPolicy
	ServiceAccount     string
	DatabaseSecretName string
	DatabaseSecretKey  string
	BackoffLimit       int32
	TTLSecondsAfter    int32
	ActiveDeadlineSecs int64
	Resources          corev1.ResourceRequirements
}

func (c TrainingJobCreator) Create(ctx context.Context, run training.Job) error {
	image := c.imageForEngine(run.Engine)
	if c.Client == nil || c.Namespace == "" || image == "" || c.DatabaseSecretName == "" || c.DatabaseSecretKey == "" || strings.TrimSpace(run.PolicyRevision) == "" {
		return fmt.Errorf("training Job creator requires client, namespace, image, database Secret reference, and policy revision")
	}
	backoff := c.BackoffLimit
	if backoff < 0 {
		backoff = 0
	}
	ttl := c.TTLSecondsAfter
	if ttl <= 0 {
		ttl = 3600
	}
	deadline := c.ActiveDeadlineSecs
	if deadline <= 0 {
		deadline = 3600
	}
	pullPolicy := c.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}
	if pullPolicy != corev1.PullAlways && pullPolicy != corev1.PullIfNotPresent && pullPolicy != corev1.PullNever {
		return fmt.Errorf("training Job creator has invalid image pull policy %q", pullPolicy)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: trainingJobName(run.RunID), Namespace: c.Namespace, Labels: map[string]string{"app": "replicasense-trainer", "replicasense.keda.sh/training-run": run.RunID}},
		Spec: batchv1.JobSpec{BackoffLimit: &backoff, TTLSecondsAfterFinished: &ttl, ActiveDeadlineSeconds: &deadline, Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "replicasense-trainer"}},
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: c.ServiceAccount, AutomountServiceAccountToken: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, Containers: []corev1.Container{{Name: "trainer", Image: image, ImagePullPolicy: pullPolicy, Resources: c.Resources, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}, Env: []corev1.EnvVar{
				{Name: "REPLICASENSE_DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: c.DatabaseSecretName}, Key: c.DatabaseSecretKey}}}, {Name: "REPLICASENSE_CLUSTER_ID", Value: run.ClusterID}, {Name: "REPLICASENSE_WORKLOAD_ID", Value: run.WorkloadID}, {Name: "REPLICASENSE_TRAINING_RUN_ID", Value: run.RunID}, {Name: "REPLICASENSE_MODEL_ENGINE", Value: run.Engine}, {Name: "REPLICASENSE_POLICY_REVISION", Value: run.PolicyRevision},
			}}},
			}},
		},
	}
	if _, err := c.Client.BatchV1().Jobs(c.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create trainer Job: %w", err)
	}
	return nil
}

func (c TrainingJobCreator) imageForEngine(engine string) string {
	if image := strings.TrimSpace(c.EngineImages[strings.TrimSpace(engine)]); image != "" {
		return image
	}
	return strings.TrimSpace(c.Image)
}

func trainingJobName(runID string) string {
	name := "replicasense-trainer-" + strings.ToLower(strings.TrimSpace(runID))
	if len(name) > 63 {
		return name[:63]
	}
	return name
}
