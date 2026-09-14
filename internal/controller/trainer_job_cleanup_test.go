package controller

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTrainerJobCleanerDeletesOnlyTerminalTrainerJobs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	completed := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "completed", Namespace: "system", Labels: map[string]string{"app": "replicasense-trainer"}}, Status: batchv1.JobStatus{CompletionTime: &metav1.Time{Time: time.Now()}}}
	failed := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "system", Labels: map[string]string{"app": "replicasense-trainer"}}, Status: batchv1.JobStatus{CompletionTime: &metav1.Time{Time: time.Now()}}}
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "system", Labels: map[string]string{"app": "replicasense-trainer"}}}
	unrelated := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "system", Labels: map[string]string{"app": "other"}}, Status: batchv1.JobStatus{CompletionTime: &metav1.Time{Time: time.Now()}}}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(completed, failed, running, unrelated).Build()

	cleaner := TrainerJobCleaner{Reader: fakeClient, Writer: fakeClient, Namespace: "system"}
	deleted, err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	for _, name := range []string{"running", "unrelated"} {
		var job batchv1.Job
		if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "system", Name: name}, &job); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
}

func TestTrainerJobCleanerNeedsLeaderElection(t *testing.T) {
	if !((TrainerJobCleaner{}).NeedLeaderElection()) {
		t.Fatal("trainer Job cleanup must run only on the elected controller")
	}
}
