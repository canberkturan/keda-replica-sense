package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TrainerJobCleaner removes only terminal, ReplicaSense-created trainer Jobs.
// It is a leader-elected manager runnable, so controller replicas never race
// to delete the same Job.
type TrainerJobCleaner struct {
	Reader    client.Reader
	Writer    client.Writer
	Namespace string
	Interval  time.Duration
	Log       logr.Logger
}

// NeedLeaderElection ensures cleanup runs on only the active controller when
// the Helm deployment has more than one replica.
func (TrainerJobCleaner) NeedLeaderElection() bool { return true }

func (c TrainerJobCleaner) Start(ctx context.Context) error {
	if c.Reader == nil || c.Writer == nil || c.Namespace == "" {
		return fmt.Errorf("trainer Job cleaner requires Kubernetes reader, writer, and namespace")
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	for {
		if deleted, err := c.Cleanup(ctx); err != nil {
			c.Log.Error(err, "clean completed trainer Jobs", "namespace", c.Namespace)
		} else if deleted > 0 {
			c.Log.Info("cleaned completed trainer Jobs", "namespace", c.Namespace, "count", deleted)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// Cleanup deletes only terminal Jobs bearing the ReplicaSense trainer label.
// It deliberately leaves active Jobs and every Job owned by another component.
func (c TrainerJobCleaner) Cleanup(ctx context.Context) (int, error) {
	if c.Reader == nil || c.Writer == nil || c.Namespace == "" {
		return 0, fmt.Errorf("trainer Job cleaner requires Kubernetes reader, writer, and namespace")
	}
	var jobs batchv1.JobList
	if err := c.Reader.List(ctx, &jobs, client.InNamespace(c.Namespace), client.MatchingLabels{"app": "replicasense-trainer"}); err != nil {
		return 0, fmt.Errorf("list trainer Jobs: %w", err)
	}
	deleted := 0
	for index := range jobs.Items {
		job := &jobs.Items[index]
		if job.Status.CompletionTime == nil {
			continue
		}
		if err := c.Writer.Delete(ctx, job); err != nil {
			return deleted, fmt.Errorf("delete trainer Job %s: %w", job.Name, err)
		}
		deleted++
	}
	return deleted, nil
}
