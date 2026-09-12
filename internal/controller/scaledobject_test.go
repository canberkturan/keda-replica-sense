package controller

import (
	"context"
	"testing"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/canberkturan/keda-replica-sense/internal/scaledobject"
)

func TestProcessorObservesParsedWorkload(t *testing.T) {
	sink := &recordingSink{}
	processor := Processor{
		ParserOptions: scaledobject.ParserOptions{ClusterID: "cluster-a", ScalerAddresses: map[string]struct{}{"replicasense.default.svc:6000": {}}, DefaultMaxReplicaCount: 100},
		Sink:          sink,
	}
	object := predictiveScaledObject()
	if err := processor.ObserveScaledObject(context.Background(), object); err != nil {
		t.Fatalf("ObserveScaledObject() error = %v", err)
	}
	if len(sink.observations) != 1 || sink.observations[0].Result.Candidates[0].Spec == nil {
		t.Fatalf("unexpected observations: %#v", sink.observations)
	}
	if sink.observations[0].Source.ClusterID != "cluster-a" || sink.observations[0].Source.UID != "uid-1" {
		t.Fatalf("unexpected source: %#v", sink.observations[0].Source)
	}
}

func TestProcessorDeactivatesDeletedScaledObject(t *testing.T) {
	sink := &recordingSink{}
	processor := Processor{ParserOptions: scaledobject.ParserOptions{ClusterID: "cluster-a"}, Sink: sink}
	if err := processor.DeactivateScaledObject(context.Background(), "default", "payments-api"); err != nil {
		t.Fatalf("DeactivateScaledObject() error = %v", err)
	}
	if len(sink.deactivated) != 1 || sink.deactivated[0].ScaledObjectName != "payments-api" {
		t.Fatalf("unexpected deactivations: %#v", sink.deactivated)
	}
}

type recordingSink struct {
	observations []Observation
	deactivated  []ScaledObjectReference
}

func (s *recordingSink) Observe(_ context.Context, observation Observation) error {
	s.observations = append(s.observations, observation)
	return nil
}

func (s *recordingSink) DeactivateScaledObject(_ context.Context, reference ScaledObjectReference) error {
	s.deactivated = append(s.deactivated, reference)
	return nil
}

func predictiveScaledObject() *kedav1alpha1.ScaledObject {
	min, max := int32(3), int32(50)
	return &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "payments-api", UID: "uid-1"},
		Spec: kedav1alpha1.ScaledObjectSpec{
			ScaleTargetRef:  &kedav1alpha1.ScaleTarget{Name: "payments-api"},
			MinReplicaCount: &min,
			MaxReplicaCount: &max,
			Triggers: []kedav1alpha1.ScaleTriggers{
				{Type: "prometheus", Name: "reactive", Metadata: map[string]string{"serverAddress": "http://prometheus:9090", "query": "up", "threshold": "100"}},
				{Type: "external", Name: "predictive", Metadata: map[string]string{"scalerAddress": "replicasense.default.svc:6000", "sourceTrigger": "reactive"}},
			},
		},
	}
}
