package scaler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
)

func TestSnapshotCacheRefreshesAtomicallyAndRetainsLastGoodValue(t *testing.T) {
	lookup := forecast.Lookup{ClusterID: "cluster-a", Namespace: "default", ScaledObject: "checkout", SourceTrigger: "reactive"}
	lister := &cacheLister{snapshots: []forecast.SnapshotWithLookup{{Lookup: lookup, Snapshot: domain.ForecastSnapshot{SafeDemand: 4, GeneratedAt: time.Now()}}}}
	cache := NewSnapshotCache("cluster-a", lister)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	snapshot, err := cache.LatestSnapshot(context.Background(), lookup)
	if err != nil || snapshot.SafeDemand != 4 {
		t.Fatalf("LatestSnapshot() = %#v, %v; want demand 4", snapshot, err)
	}
	lister.err = errors.New("database unavailable")
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil, want database error")
	}
	snapshot, err = cache.LatestSnapshot(context.Background(), lookup)
	if err != nil || snapshot.SafeDemand != 4 {
		t.Fatalf("cache lost last good snapshot: %#v, %v", snapshot, err)
	}
}

func TestSnapshotCacheClearsEntriesOnSuccessfulRefresh(t *testing.T) {
	lookup := forecast.Lookup{ClusterID: "cluster-a", Namespace: "default", ScaledObject: "checkout", SourceTrigger: "reactive"}
	lister := &cacheLister{snapshots: []forecast.SnapshotWithLookup{{Lookup: lookup, Snapshot: domain.ForecastSnapshot{SafeDemand: 4}}}}
	cache := NewSnapshotCache("cluster-a", lister)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lister.snapshots = nil
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.LatestSnapshot(context.Background(), lookup); err == nil {
		t.Fatal("LatestSnapshot() error = nil, want cache miss")
	}
}

type cacheLister struct {
	snapshots []forecast.SnapshotWithLookup
	err       error
}

func (l *cacheLister) ListLatestSnapshots(context.Context, string) ([]forecast.SnapshotWithLookup, error) {
	return l.snapshots, l.err
}
