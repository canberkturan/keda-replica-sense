package forecaster

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

type ActiveModelReader interface {
	ListActiveModels(context.Context, string) ([]forecast.StoredModel, error)
}

// ModelCache is refreshed off the inference path. Failed refreshes retain the
// last good cache; a workload with no active model uses the mandatory baseline.
type cachedModel struct {
	id          string
	model       forecast.Model
	activatedAt time.Time
}

type ModelCache struct {
	clusterID string
	reader    ActiveModelReader
	maxAge    time.Duration
	mu        sync.RWMutex
	models    map[string]cachedModel
}

func NewModelCache(clusterID string, reader ActiveModelReader, maxAge time.Duration) *ModelCache {
	return &ModelCache{clusterID: clusterID, reader: reader, maxAge: maxAge, models: make(map[string]cachedModel)}
}

func (c *ModelCache) Refresh(ctx context.Context) error {
	if c.reader == nil || c.clusterID == "" {
		return fmt.Errorf("model cache requires reader and cluster ID")
	}
	stored, err := c.reader.ListActiveModels(ctx, c.clusterID)
	if err != nil {
		return err
	}
	// A cache refresh is frequent. Retain unchanged immutable artifacts instead
	// of repeatedly deserializing a C-backed model on the inference process.
	c.mu.RLock()
	previous := make(map[string]cachedModel, len(c.models))
	for key, cached := range c.models {
		previous[key] = cached
	}
	c.mu.RUnlock()
	next := make(map[string]cachedModel, len(stored))
	var loadErrors []error
	for _, item := range stored {
		key := item.WorkloadID + ":" + item.SourceFingerprint
		if cached, ok := previous[key]; ok && cached.id == item.ID {
			next[key] = cached
			continue
		}
		model, err := forecast.LoadModel(item)
		if err != nil {
			// One corrupt or legacy artifact must not prevent valid current
			// candidates from loading. Omitting it makes that workload take the
			// existing baseline/fallback path rather than serving unknown data.
			loadErrors = append(loadErrors, fmt.Errorf("load active model %s: %w", item.ID, err))
			continue
		}
		next[key] = cachedModel{id: item.ID, model: model, activatedAt: item.ActivatedAt}
	}
	c.mu.Lock()
	c.models = next
	c.mu.Unlock()
	return errors.Join(loadErrors...)
}

func (c *ModelCache) ModelFor(workload workloadstore.SamplingWorkload) (forecast.Model, bool) {
	c.mu.RLock()
	cached, ok := c.models[workload.ID+":"+workload.Spec.SourceFingerprint]
	c.mu.RUnlock()
	if !ok || (c.maxAge > 0 && (cached.activatedAt.IsZero() || time.Since(cached.activatedAt) > c.maxAge)) {
		return nil, false
	}
	if configured := workload.Spec.Forecast.ModelEngine; configured != "" && cached.model.Engine() != configured {
		return nil, false
	}
	return cached.model, true
}
