package scaledobject

import (
	"errors"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
)

// InputFromKEDA converts the KEDA API object at the integration boundary. The
// parser itself intentionally operates only on ScaledObjectInput, which keeps
// its tests free from Kubernetes API dependencies.
func InputFromKEDA(object *kedav1alpha1.ScaledObject) (ScaledObjectInput, error) {
	if object == nil {
		return ScaledObjectInput{}, errors.New("scaled object is nil")
	}

	input := ScaledObjectInput{
		Namespace:       object.Namespace,
		Name:            object.Name,
		UID:             string(object.UID),
		Generation:      object.Generation,
		MinReplicaCount: copyInt32(object.Spec.MinReplicaCount),
		MaxReplicaCount: copyInt32(object.Spec.MaxReplicaCount),
		Triggers:        make([]TriggerInput, 0, len(object.Spec.Triggers)),
	}
	if object.Spec.ScaleTargetRef != nil {
		input.ScaleTargetName = object.Spec.ScaleTargetRef.Name
	}
	for _, trigger := range object.Spec.Triggers {
		input.Triggers = append(input.Triggers, TriggerInput{
			Type:     trigger.Type,
			Name:     trigger.Name,
			Metadata: copyMetadata(trigger.Metadata),
		})
	}
	return input, nil
}

func copyInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copy[key] = value
	}
	return copy
}
