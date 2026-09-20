package sampler

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type Service struct {
	Store          Store
	Querier        PrometheusQuerier
	ClusterID      string
	MaxConcurrent  int
	QueryTimeout   time.Duration
	MaxRetries     int
	InitialBackoff time.Duration
	JitterWindow   time.Duration
	Backfiller     *Backfiller
	bootstrapped   sync.Map
	failed         sync.Map
	Sleep          func(context.Context, time.Duration) error
	Observer       interface {
		RecordSample(workloadstore.SamplingWorkload, float64)
	}
}

// RunOnce queries every active workload in the local cluster. Successful
// observations are persisted even if other workload queries fail.
func (s *Service) RunOnce(ctx context.Context, observedAt time.Time) error {
	if s.Store == nil || s.Querier == nil {
		return errors.New("sampler store and Prometheus querier are required")
	}
	workloads, err := s.Store.ListActiveForCluster(ctx, s.ClusterID)
	if err != nil {
		return err
	}
	limit := s.MaxConcurrent
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var mutex sync.Mutex
	var samples []domain.Sample
	var failures []error
	var group sync.WaitGroup
	for _, workload := range workloads {
		workload := workload
		if err := s.ensureBootstrap(ctx, workload, observedAt); err != nil {
			failures = append(failures, err)
			continue
		}
		if !s.isDue(workload, observedAt) {
			continue
		}
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				mutex.Lock()
				failures = append(failures, ctx.Err())
				mutex.Unlock()
				return
			}
			value, err := s.queryWithRetry(ctx, workload, observedAt)
			if err != nil {
				s.failed.Store(workload.ID+":"+workload.Spec.SourceFingerprint, struct{}{})
			}
			recovered := false
			if err == nil {
				_, recovered = s.failed.LoadAndDelete(workload.ID + ":" + workload.Spec.SourceFingerprint)
				if recovered && s.Backfiller != nil {
					// Best effort: an unavailable historical range must not discard
					// the newly recovered live sample.
					if _, recoveryErr := s.Backfiller.RecoverRecentHistory(ctx, workload, observedAt); recoveryErr != nil {
						mutex.Lock()
						failures = append(failures, fmt.Errorf("recover workload %s history: %w", workload.ID, recoveryErr))
						mutex.Unlock()
					}
				}
			}
			mutex.Lock()
			defer mutex.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			samples = append(samples, BuildSample(workload, observedAt, value))
			if s.Observer != nil {
				s.Observer.RecordSample(workload, value)
			}
		}()
	}
	group.Wait()
	if err := s.Store.UpsertSamples(ctx, samples); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Service) ensureBootstrap(ctx context.Context, workload workloadstore.SamplingWorkload, now time.Time) error {
	if s.Backfiller == nil {
		return nil
	}
	key := workload.ID + ":" + workload.Spec.SourceFingerprint
	if _, ok := s.bootstrapped.Load(key); ok {
		return nil
	}
	if _, err := s.Backfiller.EnsureHistory(ctx, workload, now); err != nil {
		return fmt.Errorf("bootstrap workload %s: %w", workload.ID, err)
	}
	s.bootstrapped.Store(key, struct{}{})
	return nil
}

func (s *Service) isDue(workload workloadstore.SamplingWorkload, observedAt time.Time) bool {
	interval := workload.Spec.Forecast.SamplingInterval
	if interval <= 0 {
		return false
	}
	intervalSeconds := int64(interval / time.Second)
	if intervalSeconds < 1 {
		return false
	}
	second := observedAt.UTC().Unix()
	cycleStart := second - second%intervalSeconds
	return second == cycleStart+int64(s.jitterFor(workload)/time.Second)
}

// jitterFor returns a repeatable delay for one workload. Keeping this a pure
// function makes timing stable across sampler restarts and straightforward to
// test. The window never exceeds the workload's configured sample interval.
func (s *Service) jitterFor(workload workloadstore.SamplingWorkload) time.Duration {
	window := s.JitterWindow
	if interval := workload.Spec.Forecast.SamplingInterval; interval > 0 && (window <= 0 || interval < window) {
		window = interval
	}
	if window <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(workload.ID))
	seconds := int64(window / time.Second)
	if seconds < 1 {
		return 0
	}
	return time.Duration(hash.Sum64()%uint64(seconds)) * time.Second
}

func (s *Service) queryWithRetry(ctx context.Context, workload workloadstore.SamplingWorkload, observedAt time.Time) (float64, error) {
	attempts := s.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastError error
	timeout := s.QueryTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	for attempt := 0; attempt < attempts; attempt++ {
		queryContext, cancel := context.WithTimeout(ctx, timeout)
		value, err := s.Querier.QueryInstant(queryContext, PrometheusQuery{ServerAddress: workload.Spec.Source.ServerAddress, Query: workload.Spec.Source.Query, ObservedAt: observedAt})
		cancel()
		if err == nil {
			return value, nil
		}
		lastError = err
		if attempt+1 == attempts {
			break
		}
		backoff := s.InitialBackoff * time.Duration(1<<attempt)
		if backoff > 0 {
			if err := s.sleep(ctx, backoff); err != nil {
				return 0, err
			}
		}
	}
	return 0, fmt.Errorf("sample workload %s: %w", workload.ID, lastError)
}

func (s *Service) sleep(ctx context.Context, duration time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
