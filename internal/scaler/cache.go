package scaler

import (
	"context"
	"fmt"
	"sync"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
)

// SnapshotCache is a read-only, atomically refreshed cache for the external
// scaler. A failed refresh preserves the last known snapshots; freshness is
// still enforced by Server before any metric is returned.
type SnapshotCache struct {
	clusterID string
	lister    forecast.SnapshotLister
	mu        sync.RWMutex
	entries   map[forecast.Lookup]domain.ForecastSnapshot
}

func NewSnapshotCache(clusterID string, lister forecast.SnapshotLister) *SnapshotCache {
	return &SnapshotCache{clusterID: clusterID, lister: lister, entries: make(map[forecast.Lookup]domain.ForecastSnapshot)}
}

func (c *SnapshotCache) Refresh(ctx context.Context) error {
	if c.lister == nil || c.clusterID == "" {
		return fmt.Errorf("snapshot cache requires cluster ID and lister")
	}
	snapshots, err := c.lister.ListLatestSnapshots(ctx, c.clusterID)
	if err != nil {
		return err
	}
	next := make(map[forecast.Lookup]domain.ForecastSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		next[snapshot.Lookup] = snapshot.Snapshot
	}
	c.mu.Lock()
	c.entries = next
	c.mu.Unlock()
	return nil
}

func (c *SnapshotCache) LatestSnapshot(_ context.Context, lookup forecast.Lookup) (*domain.ForecastSnapshot, error) {
	c.mu.RLock()
	snapshot, ok := c.entries[lookup]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("snapshot not cached")
	}
	return &snapshot, nil
}
