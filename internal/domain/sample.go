package domain

import "time"

// Sample is one canonical demand observation. ObservedAt is the query time,
// not the time PostgreSQL happened to receive the row.
type Sample struct {
	ClusterID         string
	WorkloadID        string
	SourceFingerprint string
	ObservedAt        time.Time
	ObservedValue     float64
}
