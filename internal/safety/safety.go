// Package safety contains deterministic controls around untrusted forecasts.
package safety

import "math"

type SurgeInput struct {
	CurrentValue          float64
	RecentBaseline        float64
	CurrentSlopePerMinute float64
	HistoricalSlopeP95    float64
	LeadTimeMinutes       float64
}

// SurgePolicy controls deterministic protection for demand patterns that are
// not represented by the trained model. The defaults deliberately favor a
// sustained, material increase over reacting to one noisy observation.
type SurgePolicy struct {
	SlopeMultiplier     float64
	BaselineMultiplier  float64
	ConfirmationSamples int
	MaxMultiplier       float64
}

func DefaultSurgePolicy() SurgePolicy {
	return SurgePolicy{
		SlopeMultiplier:     3,
		BaselineMultiplier:  1.5,
		ConfirmationSamples: 2,
		MaxMultiplier:       1.5,
	}
}

type SurgeResult struct {
	Triggered bool
	Demand    float64
	Reason    string
}

// DetectSurge predicts only across pod readiness lead time, never the full ML
// horizon. This prevents a short spike from becoming an unbounded extrapolation.
func DetectSurge(in SurgeInput, policy SurgePolicy) SurgeResult {
	if in.LeadTimeMinutes <= 0 {
		return SurgeResult{}
	}
	policy = normalizedSurgePolicy(policy)
	if !IsSurgeCandidate(in, policy) {
		return SurgeResult{}
	}
	demand := in.CurrentValue + math.Max(0, in.CurrentSlopePerMinute)*in.LeadTimeMinutes
	if policy.MaxMultiplier > 1 {
		demand = math.Min(demand, in.CurrentValue*policy.MaxMultiplier)
	}
	return SurgeResult{true, demand, "sustained_abnormal_slope_and_baseline"}
}

// IsSurgeCandidate requires both fast growth compared with history and a
// material move above the recent baseline. A flat historical slope treats any
// positive slope as anomalous, but only together with the baseline test.
func IsSurgeCandidate(in SurgeInput, policy SurgePolicy) bool {
	policy = normalizedSurgePolicy(policy)
	if in.CurrentSlopePerMinute <= 0 || in.RecentBaseline <= 0 {
		return false
	}
	slopeAbnormal := in.HistoricalSlopeP95 <= 0 || in.CurrentSlopePerMinute >= in.HistoricalSlopeP95*policy.SlopeMultiplier
	baselineAbnormal := in.CurrentValue >= in.RecentBaseline*policy.BaselineMultiplier
	return slopeAbnormal && baselineAbnormal
}

func normalizedSurgePolicy(policy SurgePolicy) SurgePolicy {
	defaults := DefaultSurgePolicy()
	if policy.SlopeMultiplier <= 1 {
		policy.SlopeMultiplier = defaults.SlopeMultiplier
	}
	if policy.BaselineMultiplier <= 1 {
		policy.BaselineMultiplier = defaults.BaselineMultiplier
	}
	if policy.ConfirmationSamples < 1 {
		policy.ConfirmationSamples = defaults.ConfirmationSamples
	}
	if policy.MaxMultiplier <= 1 {
		policy.MaxMultiplier = defaults.MaxMultiplier
	}
	return policy
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
