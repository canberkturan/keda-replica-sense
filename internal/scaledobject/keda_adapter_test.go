package scaledobject

import (
	"testing"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestInputFromKEDA(t *testing.T) {
	min, max := int32(3), int32(50)
	object := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "default",
			Name:       "payments-api",
			UID:        types.UID("example-uid"),
			Generation: 7,
		},
		Spec: kedav1alpha1.ScaledObjectSpec{
			ScaleTargetRef:  &kedav1alpha1.ScaleTarget{Name: "payments-api"},
			MinReplicaCount: &min,
			MaxReplicaCount: &max,
			Triggers: []kedav1alpha1.ScaleTriggers{{
				Type:     "prometheus",
				Name:     "reactive",
				Metadata: map[string]string{"query": "up"},
			}},
		},
	}

	input, err := InputFromKEDA(object)
	if err != nil {
		t.Fatalf("InputFromKEDA() error = %v", err)
	}
	if input.Namespace != "default" || input.Name != "payments-api" || input.UID != "example-uid" || input.Generation != 7 {
		t.Fatalf("unexpected object metadata: %#v", input)
	}
	if input.ScaleTargetName != "payments-api" || *input.MinReplicaCount != 3 || *input.MaxReplicaCount != 50 {
		t.Fatalf("unexpected scale target or bounds: %#v", input)
	}
	if len(input.Triggers) != 1 || input.Triggers[0].Metadata["query"] != "up" {
		t.Fatalf("unexpected triggers: %#v", input.Triggers)
	}

	object.Spec.Triggers[0].Metadata["query"] = "changed"
	*object.Spec.MinReplicaCount = 99
	if input.Triggers[0].Metadata["query"] != "up" || *input.MinReplicaCount != 3 {
		t.Fatal("adapter output must not alias the source API object")
	}
}

func TestInputFromKEDANil(t *testing.T) {
	if _, err := InputFromKEDA(nil); err == nil {
		t.Fatal("InputFromKEDA(nil) error = nil, want error")
	}
}
