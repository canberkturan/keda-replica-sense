package domain

import (
	"testing"
	"time"
)

func TestForecastDecisionHorizonIncludesCapacityReadiness(t *testing.T) {
	forecast := ForecastConfig{Horizon: 5 * time.Minute, StartupLatency: 2 * time.Minute, SafetyBuffer: time.Minute}
	if got, want := forecast.DecisionHorizon(), 8*time.Minute; got != want {
		t.Fatalf("DecisionHorizon() = %s, want %s", got, want)
	}
}
