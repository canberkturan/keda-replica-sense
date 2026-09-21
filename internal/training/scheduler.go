// Package training coordinates durable, per-workload training runs.
package training

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
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
	// ExclusiveSince prevents a scheduled run from following an immediate run
	// in the same schedule interval.
	ExclusiveSince time.Time
}

type Run struct{ ID string }

type ClaimStore interface {
	Claim(context.Context, Claim) (*Run, bool, error)
	ClaimImmediate(context.Context, Claim) (*Run, bool, error)
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
	// PolicyRevision is checked by the trainer before it can publish a model.
	// A Job created for an older ScaledObject revision cannot reintroduce a
	// model after clearOldModels has replaced that contract.
	PolicyRevision string
}

type JobCreator interface {
	Create(context.Context, Job) error
}

// Scheduler uses a durable slot claim before creating a Job. This makes
// concurrent schedulers harmless without a second distributed lock service.
type Scheduler struct {
	Workloads        WorkloadLister
	History          HistoryChecker
	Claims           ClaimStore
	Jobs             JobCreator
	ClusterID        string
	TrainingInterval time.Duration
}

// HistoryChecker confirms that the sampler has materialized a complete enough
// training window before a one-time immediate Job is claimed.
type HistoryChecker interface {
	GetSampleCoverage(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) (workloadstore.SampleCoverage, error)
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
		if workload.Spec.Forecast.ImmediateTraining {
			end := now.UTC().Truncate(workload.Spec.Forecast.SamplingInterval)
			start := end.Add(-workload.Spec.Forecast.TrainingWindow)
			ready, err := s.immediateHistoryReady(ctx, workload, start, end)
			if err != nil {
				return fmt.Errorf("check immediate training history for %s: %w", workload.ID, err)
			}
			if !ready {
				continue
			}
			claim := Claim{ClusterID: s.ClusterID, WorkloadID: workload.ID, Slot: immediateSlot(workload.Spec.PolicyRevision), Engine: workload.Spec.Forecast.ModelEngine, TrainingWindowFrom: start, TrainingWindowTo: end}
			run, ready, err := s.Claims.ClaimImmediate(ctx, claim)
			if err != nil {
				return fmt.Errorf("claim immediate training for %s: %w", workload.ID, err)
			}
			if ready {
				if err := s.Jobs.Create(ctx, Job{RunID: run.ID, ClusterID: s.ClusterID, WorkloadID: workload.ID, Engine: claim.Engine, PolicyRevision: workload.Spec.PolicyRevision}); err != nil {
					if markErr := s.Claims.MarkFailed(ctx, run.ID, err.Error()); markErr != nil {
						return fmt.Errorf("create immediate Job: %v; mark failed: %w", err, markErr)
					}
					return fmt.Errorf("create immediate Job: %w", err)
				}
			}
		}
		if now.UTC().Before(slot.Add(offset(workload.ID, interval))) {
			continue
		}
		claim := Claim{ClusterID: s.ClusterID, WorkloadID: workload.ID, Slot: slot, Engine: workload.Spec.Forecast.ModelEngine, TrainingWindowFrom: now.UTC().Add(-workload.Spec.Forecast.TrainingWindow), TrainingWindowTo: now.UTC(), ExclusiveSince: slot}
		run, claimed, err := s.Claims.Claim(ctx, claim)
		if err != nil {
			return fmt.Errorf("claim training for %s: %w", workload.ID, err)
		}
		if !claimed {
			continue
		}
		if err := s.Jobs.Create(ctx, Job{RunID: run.ID, ClusterID: s.ClusterID, WorkloadID: workload.ID, Engine: claim.Engine, PolicyRevision: workload.Spec.PolicyRevision}); err != nil {
			if markErr := s.Claims.MarkFailed(ctx, run.ID, err.Error()); markErr != nil {
				return fmt.Errorf("create Job: %v; mark failed: %w", err, markErr)
			}
			return fmt.Errorf("create Job: %w", err)
		}
	}
	return nil
}

func (s Scheduler) immediateHistoryReady(ctx context.Context, workload workloadstore.SamplingWorkload, start, end time.Time) (bool, error) {
	if s.History == nil {
		return false, fmt.Errorf("scheduler requires history checker for immediate training")
	}
	coverage, err := s.History.GetSampleCoverage(ctx, workload, start, end)
	if err != nil {
		return false, err
	}
	return hasSufficientHistory(coverage, start, end, workload.Spec.Forecast.SamplingInterval), nil
}

func hasSufficientHistory(coverage workloadstore.SampleCoverage, start, end time.Time, interval time.Duration) bool {
	if coverage.Count == 0 || interval <= 0 || end.Before(start) {
		return false
	}
	expected := int64(end.Sub(start) / interval)
	// Prometheus queries are not a clock. At high sampling rates, a small
	// number of scrape-alignment or scheduler-jitter gaps is normal; requiring
	// every slot would keep an otherwise complete immediate-training window
	// permanently pending. Keep a strong 99.8% coverage requirement, while the
	// endpoint checks below still reject a stale or truncated history.
	minimumCount := int64(math.Ceil(float64(expected) * 0.998))
	// Preserve the two-sample allowance for short windows; for long windows,
	// the percentage limit is the more realistic and still conservative guard.
	if strictMinimum := expected - 2; strictMinimum < minimumCount {
		minimumCount = strictMinimum
	}
	if minimumCount < 1 {
		minimumCount = 1
	}
	return coverage.Count >= minimumCount && !coverage.OldestObservedAt.After(start.Add(interval)) && !coverage.NewestObservedAt.Before(end.Add(-2*interval))
}

// immediateSlot makes a policy revision a durable, idempotent claim key in a
// reserved future range. The same configuration cannot enqueue another
// immediate run on every reconciliation or collide with a current schedule
// slot.
func immediateSlot(policyRevision string) time.Time {
	sum := sha256.Sum256([]byte(policyRevision))
	const century = uint64(100 * 365 * 24 * 60 * 60)
	seconds := binary.BigEndian.Uint64(sum[:8]) % century
	return time.Date(2500, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seconds) * time.Second)
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
