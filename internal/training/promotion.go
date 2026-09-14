package training

import (
	"fmt"
	"math"

	"github.com/canberkturan/keda-replica-sense/internal/forecast"
)

// PromotionPolicy favours avoiding an unsafe underprediction over marginal
// generic accuracy gains. MinimumImprovement is a fractional pinball-loss
// improvement required against an existing comparable reference.
type PromotionPolicy struct {
	MinimumCoverage        float64
	MaximumUnderprediction float64
	MinimumImprovement     float64
}

func DefaultPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{MinimumCoverage: 0.93, MaximumUnderprediction: 0.07, MinimumImprovement: 0}
}

type PromotionDecision struct {
	Promote   bool   `json:"promote"`
	Reason    string `json:"reason"`
	Reference string `json:"reference"`
}

// EvaluatePromotion applies absolute safety limits first, then compares the
// candidate to a no-leakage validation reference. A champion is preferred; a
// deterministic baseline is used only when no comparable champion exists.
func EvaluatePromotion(policy PromotionPolicy, candidate forecast.ValidationMetrics, champion *forecast.ValidationMetrics, baseline forecast.ValidationMetrics) PromotionDecision {
	if policy.MinimumCoverage <= 0 || policy.MinimumCoverage > 1 {
		policy.MinimumCoverage = DefaultPromotionPolicy().MinimumCoverage
	}
	if policy.MaximumUnderprediction <= 0 || policy.MaximumUnderprediction > 1 {
		policy.MaximumUnderprediction = DefaultPromotionPolicy().MaximumUnderprediction
	}
	if policy.MinimumImprovement < 0 || policy.MinimumImprovement >= 1 {
		policy.MinimumImprovement = 0
	}
	if !validMetrics(candidate) {
		return PromotionDecision{Reason: "candidate_validation_invalid"}
	}
	if candidate.Coverage < policy.MinimumCoverage {
		return PromotionDecision{Reason: fmt.Sprintf("coverage_below_minimum: %.4f < %.4f", candidate.Coverage, policy.MinimumCoverage)}
	}
	if candidate.UnderpredictionRate > policy.MaximumUnderprediction {
		return PromotionDecision{Reason: fmt.Sprintf("underprediction_above_maximum: %.4f > %.4f", candidate.UnderpredictionRate, policy.MaximumUnderprediction)}
	}
	reference, name := baseline, "deterministic_baseline"
	if champion != nil && validMetrics(*champion) {
		reference, name = *champion, "active_champion"
	}
	if !noWorseThanReference(policy, candidate, reference) {
		return PromotionDecision{Reason: "not_better_than_" + name, Reference: name}
	}
	return PromotionDecision{Promote: true, Reason: "promotion_policy_satisfied", Reference: name}
}

func validMetrics(metrics forecast.ValidationMetrics) bool {
	return metrics.Windows > 0 && metrics.Coverage >= 0 && metrics.Coverage <= 1 && metrics.UnderpredictionRate >= 0 && metrics.UnderpredictionRate <= 1 && finite(metrics.MeanAbsoluteError) && finite(metrics.PinballLossP95)
}

func noWorseThanReference(policy PromotionPolicy, candidate, reference forecast.ValidationMetrics) bool {
	const epsilon = 1e-9
	// Underprediction and quantile pinball loss are more important than MAE for
	// an autoscaler. MAE breaks an otherwise equal operational comparison.
	if candidate.UnderpredictionRate > reference.UnderpredictionRate+epsilon {
		return false
	}
	maximumPinball := reference.PinballLossP95 * (1 - policy.MinimumImprovement)
	if candidate.PinballLossP95 > maximumPinball+epsilon {
		return false
	}
	if math.Abs(candidate.PinballLossP95-maximumPinball) <= epsilon && candidate.MeanAbsoluteError > reference.MeanAbsoluteError+epsilon {
		return false
	}
	return true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
