// Package scaledobject parses the subset of a KEDA ScaledObject relevant to
// ReplicaSense. Kubernetes/KEDA API conversion will be added at the controller
// boundary in a later milestone.
package scaledobject

import (
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// ScaledObjectInput is a dependency-free view of a native KEDA ScaledObject.
type ScaledObjectInput struct {
	Namespace       string
	Name            string
	UID             string
	Generation      int64
	ScaleTargetName string
	MinReplicaCount *int32
	MaxReplicaCount *int32
	Triggers        []TriggerInput
}

type TriggerInput struct {
	Type     string
	Name     string
	Metadata map[string]string
}

type ParserOptions struct {
	ClusterID              string
	ScalerAddresses        map[string]struct{}
	DefaultMinReplicaCount int32
	DefaultMaxReplicaCount int32
	// MinimumTrainingWindow protects production installations from creating
	// models from an insufficient history. A zero value keeps the safe 7-day
	// default; operators may lower it only for deliberately accelerated labs.
	MinimumTrainingWindow time.Duration
}

type Violation struct {
	Field   string
	Message string
}

// Candidate is emitted for every ReplicaSense predictive trigger that can be
// identified. Spec is nil if the candidate failed validation.
type Candidate struct {
	PredictiveTriggerName string
	Spec                  *domain.WorkloadSpec
	Violations            []Violation
}

type ParseResult struct {
	Candidates []Candidate
	Violations []Violation
}
