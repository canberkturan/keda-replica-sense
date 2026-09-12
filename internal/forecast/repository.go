package forecast

import (
	"context"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

type Lookup struct {
	ClusterID     string
	Namespace     string
	ScaledObject  string
	SourceTrigger string
}

type SnapshotRepository interface {
	SaveSnapshot(context.Context, domain.ForecastSnapshot) error
	LatestSnapshot(context.Context, Lookup) (*domain.ForecastSnapshot, error)
}

// SnapshotWithLookup is a snapshot paired with the KEDA identity used by the
// external scaler. It is intentionally cluster-local and contains no metric
// query or credential material.
type SnapshotWithLookup struct {
	Lookup   Lookup
	Snapshot domain.ForecastSnapshot
}

// SnapshotLister supplies the scaler cache with a periodic full refresh. The
// gRPC request path must not query PostgreSQL directly.
type SnapshotLister interface {
	ListLatestSnapshots(context.Context, string) ([]SnapshotWithLookup, error)
}
