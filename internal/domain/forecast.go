package domain

import "time"

// ForecastSnapshot is an immutable prediction suitable for evaluation and for
// the scaler only after deterministic safety approval.
type ForecastSnapshot struct {
	ClusterID         string
	WorkloadID        string
	SourceFingerprint string
	GeneratedAt       time.Time
	ObservedAt        time.Time
	ModelEngine       string
	ForecastHorizon   time.Duration
	ForecastP50       float64
	// ForecastP95 is retained for storage/API compatibility and represents the
	// workload's configured upper operational quantile, not always literal Q95.
	ForecastP95 float64
	// SurgeDemand is a deterministic, short-term anomaly projection in demand
	// units. It is zero when the surge controller is inactive and is kept
	// separate from ForecastP95 so model evaluation remains statistically pure.
	SurgeDemand  float64
	SafeDemand   float64
	SafetyReason string
}
