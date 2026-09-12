// Package training coordinates durable, per-workload training runs.
package training

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type Claim struct {
	ClusterID          string
	WorkloadID         string
	Slot               time.Time
	Engine             string
	TrainingWindowFrom time.Time
	TrainingWindowTo   time.Time
}

type Run struct{ ID string }

type ClaimStore interface {
	Claim(context.Context, Claim) (*Run, bool, error)
	MarkFailed(context.Context, string, string) error
}

type ModelCandidate struct {
	ClusterID           string
	WorkloadID          string
	SourceFingerprint   string
	Engine              string
	FeatureSchema       string
	TrainingWindowStart time.Time
	TrainingWindowEnd   time.Time
	DatasetFingerprint  string
	Hyperparameters     []byte
	ValidationMetrics   []byte
	Artifact            []byte
	ArtifactSHA256      string
}

type WorkloadLister interface {
	ListActiveForCluster(context.Context, string) ([]workloadstore.SamplingWorkload, error)
}

type Job struct {
	RunID      string
	ClusterID  string
	WorkloadID string
	Engine     string
}

type JobCreator interface {
	Create(context.Context, Job) error
}

// Scheduler uses a durable slot claim before creating a Job. This makes
// concurrent schedulers harmless without a second distributed lock service.
type Scheduler struct {
	Workloads        WorkloadLister
	Claims           ClaimStore
	Jobs             JobCreator
	ClusterID        string
	TrainingInterval time.Duration
}

func (s Scheduler) RunOnce(ctx context.Context, now time.Time) error {
	if s.Workloads == nil || s.Claims == nil || s.Jobs == nil || s.ClusterID == "" {
		return fmt.Errorf("scheduler requires workloads, claims, jobs, and cluster ID")
	}
	interval := s.TrainingInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	workloads, err := s.Workloads.ListActiveForCluster(ctx, s.ClusterID)
	if err != nil {
		return err
	}
	slot := now.UTC().Truncate(interval)
	for _, workload := range workloads {
		if now.UTC().Before(slot.Add(offset(workload.ID, interval))) {
			continue
		}
		claim := Claim{ClusterID: s.ClusterID, WorkloadID: workload.ID, Slot: slot, Engine: workload.Spec.Forecast.ModelEngine, TrainingWindowFrom: now.UTC().Add(-workload.Spec.Forecast.TrainingWindow), TrainingWindowTo: now.UTC()}
		run, claimed, err := s.Claims.Claim(ctx, claim)
		if err != nil {
			return fmt.Errorf("claim training for %s: %w", workload.ID, err)
		}
		if !claimed {
			continue
		}
		if err := s.Jobs.Create(ctx, Job{RunID: run.ID, ClusterID: s.ClusterID, WorkloadID: workload.ID, Engine: claim.Engine}); err != nil {
			if markErr := s.Claims.MarkFailed(ctx, run.ID, err.Error()); markErr != nil {
				return fmt.Errorf("create Job: %v; mark failed: %w", err, markErr)
			}
			return fmt.Errorf("create Job: %w", err)
		}
	}
	return nil
}

func offset(workloadID string, interval time.Duration) time.Duration {
	seconds := int64(interval / time.Second)
	if seconds <= 1 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(workloadID))
	return time.Duration(hash.Sum64()%uint64(seconds)) * time.Second
}
