// Package domain contains ReplicaSense concepts that are independent of
// Kubernetes, storage, and transport implementations.
package domain

import "time"

// WorkloadKey is the stable, human-readable identity of a predictive trigger.
// A database-generated workload ID is deliberately not used as the external
// identity because records must be reconciled from Kubernetes objects.
type WorkloadKey struct {
	ClusterID             string
	Namespace             string
	ScaledObjectName      string
	PredictiveTriggerName string
}

// ScaleTarget identifies the Kubernetes workload controlled by a ScaledObject.
type ScaleTarget struct {
	Name string
}

type ReplicaBounds struct {
	Min int32
	Max int32
}

// PrometheusSource describes the signal sampled by ReplicaSense. Threshold is
// kept here for traceability but is excluded from SourceFingerprint because it
// does not change the observed signal.
type PrometheusSource struct {
	TriggerName   string
	ServerAddress string
	Query         string
	Threshold     float64
}

type ForecastConfig struct {
	Horizon          time.Duration
	Quantile         float64
	TrainingWindow   time.Duration
	SamplingInterval time.Duration
	ModelEngine      string
	BusinessTimezone string
	StartupLatency   time.Duration
	SafetyBuffer     time.Duration
	// ImmediateTraining requests one training run for this exact policy revision
	// instead of waiting for the normal schedule.
	ImmediateTraining bool
	// ClearOldModels retires all stored artifacts for this logical workload when
	// its policy revision changes. It is intentionally opt-in and irreversible.
	ClearOldModels bool
}

// DecisionHorizon is when capacity created by this decision is expected to be
// ready. Models train and forecast against this horizon, not merely the next
// polling interval.
func (f ForecastConfig) DecisionHorizon() time.Duration {
	return f.Horizon + f.StartupLatency + f.SafetyBuffer
}

// WorkloadSpec is the desired configuration derived from one predictive
// ScaledObject trigger.
type WorkloadSpec struct {
	Key               WorkloadKey
	ScaleTarget       ScaleTarget
	Bounds            ReplicaBounds
	Source            PrometheusSource
	Forecast          ForecastConfig
	SourceFingerprint string
	PolicyRevision    string
}

type WorkloadStatus string

const (
	WorkloadActive   WorkloadStatus = "active"
	WorkloadInvalid  WorkloadStatus = "invalid"
	WorkloadInactive WorkloadStatus = "inactive"
)

// Workload is the persisted lifecycle representation. ID is assigned by the
// database and remains stable while a logical workload is active.
type Workload struct {
	ID                 string
	Spec               WorkloadSpec
	ScaledObjectUID    string
	ObservedGeneration int64
	Status             WorkloadStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeactivatedAt      *time.Time
}
