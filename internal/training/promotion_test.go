package training

import (
	"strings"
	"testing"

	"github.com/canberkturan/keda-replica-sense/internal/forecast"
)

func metrics(coverage, underprediction, mae, pinball float64) forecast.ValidationMetrics {
	return forecast.ValidationMetrics{Windows: 20, Coverage: coverage, UnderpredictionRate: underprediction, MeanAbsoluteError: mae, PinballLossP95: pinball}
}

func TestEvaluatePromotion(t *testing.T) {
	baseline := metrics(.94, .06, 5, 3)
	champion := metrics(.96, .04, 2, 1)
	tests := []struct {
		name      string
		candidate forecast.ValidationMetrics
		champion  *forecast.ValidationMetrics
		promote   bool
		reason    string
	}{
		{name: "good candidate activates against baseline", candidate: metrics(.97, .03, 2, 1), promote: true, reason: "promotion_policy_satisfied"},
		{name: "poor coverage is rejected", candidate: metrics(.92, .03, 1, .5), promote: false, reason: "coverage_below_minimum"},
		{name: "excessive underprediction is rejected", candidate: metrics(.94, .08, 1, .5), promote: false, reason: "underprediction_above_maximum"},
		{name: "worse than champion does not replace", candidate: metrics(.97, .05, 1, 1.1), champion: &champion, promote: false, reason: "not_better_than_active_champion"},
		{name: "no champion uses baseline", candidate: metrics(.97, .03, 2, 1), promote: true, reason: "promotion_policy_satisfied"},
		{name: "worse than baseline is rejected", candidate: metrics(.97, .03, 6, 4), promote: false, reason: "not_better_than_deterministic_baseline"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := EvaluatePromotion(DefaultPromotionPolicy(), test.candidate, test.champion, baseline)
			if decision.Promote != test.promote || !strings.HasPrefix(decision.Reason, test.reason) {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}
