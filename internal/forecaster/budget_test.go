package forecaster

import (
	"context"
	"testing"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestSpeculativeBudgetGuard(t *testing.T) {
	w := workloadstore.SamplingWorkload{ID: "w1", Spec: domain.WorkloadSpec{Bounds: domain.ReplicaBounds{Min: 1}}}
	guard := SpeculativeBudgetGuard{Reader: fakeBudget{projected: 5}, ClusterID: "lab", MaxAdditionalReplicas: 5}
	if !guard.Allows(context.Background(), w, 6) {
		t.Fatal("budget at limit should pass")
	}
	guard.Reader = fakeBudget{projected: 6}
	if guard.Allows(context.Background(), w, 7) {
		t.Fatal("budget over limit should fail")
	}
}

type fakeBudget struct{ projected float64 }

func (f fakeBudget) ProjectedSpeculativeReplicas(context.Context, string, string, float64, int32) (float64, error) {
	return f.projected, nil
}
