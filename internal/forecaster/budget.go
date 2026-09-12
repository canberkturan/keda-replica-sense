package forecaster

import (
	"context"

	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

// SpeculativeBudgetReader projects total cluster-wide predictive capacity from
// latest safety-approved snapshots. It is used only by the forecaster.
type SpeculativeBudgetReader interface {
	ProjectedSpeculativeReplicas(context.Context, string, string, float64, int32) (float64, error)
}

type SpeculativeBudgetGuard struct {
	Reader                SpeculativeBudgetReader
	ClusterID             string
	MaxAdditionalReplicas float64
}

func (g SpeculativeBudgetGuard) Allows(ctx context.Context, workload workloadstore.SamplingWorkload, desired float64) bool {
	if g.Reader == nil || g.ClusterID == "" || g.MaxAdditionalReplicas <= 0 {
		return false
	}
	projected, err := g.Reader.ProjectedSpeculativeReplicas(ctx, g.ClusterID, workload.ID, desired, workload.Spec.Bounds.Min)
	return err == nil && projected <= g.MaxAdditionalReplicas
}
