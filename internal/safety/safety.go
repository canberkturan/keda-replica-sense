// Package safety contains deterministic controls around untrusted forecasts.
package safety

import "math"

type SurgeInput struct {
	CurrentValue          float64
	RecentBaseline        float64
	CurrentSlopePerMinute float64
	HistoricalSlopeP95    float64
	LeadTimeMinutes       float64
	MaxMultiplier         float64
}

type SurgeResult struct {
	Triggered bool
	Demand    float64
	Reason    string
}

// DetectSurge predicts only across pod readiness lead time, never the full ML
// horizon. This prevents a short spike from becoming an unbounded extrapolation.
func DetectSurge(in SurgeInput) SurgeResult {
	if in.LeadTimeMinutes <= 0 {
		return SurgeResult{}
	}
	ratio := 0.0
	if in.HistoricalSlopeP95 > 0 {
		ratio = in.CurrentSlopePerMinute / in.HistoricalSlopeP95
	}
	baselineRatio := 0.0
	if in.RecentBaseline > 0 {
		baselineRatio = in.CurrentValue / in.RecentBaseline
	}
	if ratio < 2 && baselineRatio < 3 {
		return SurgeResult{}
	}
	demand := in.CurrentValue + math.Max(0, in.CurrentSlopePerMinute)*in.LeadTimeMinutes
	if in.MaxMultiplier > 1 {
		demand = math.Min(demand, in.CurrentValue*in.MaxMultiplier)
	}
	return SurgeResult{true, demand, "abnormal_slope_or_baseline"}
}

type GuardrailInput struct {
	Desired, Current   float64
	MaxReplicas        int32
	MaxAbsoluteStep    float64
	MaxPercentIncrease float64
	ClusterHealthy     bool
	CapacityAllowed    bool
}

func ApplyGuardrails(in GuardrailInput) (float64, string) {
	if !in.ClusterHealthy {
		return in.Current, "cluster_unhealthy"
	}
	if !in.CapacityAllowed {
		return in.Current, "capacity_unavailable"
	}
	desired := math.Min(in.Desired, float64(in.MaxReplicas))
	if desired <= in.Current {
		return desired, "no_predictive_scale_up"
	}
	limit := desired
	if in.MaxAbsoluteStep > 0 {
		limit = math.Min(limit, in.Current+in.MaxAbsoluteStep)
	}
	if in.MaxPercentIncrease > 0 && in.Current > 0 {
		limit = math.Min(limit, in.Current*(1+in.MaxPercentIncrease/100))
	}
	return limit, "rate_limited"
}
