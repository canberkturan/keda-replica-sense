package main

import "testing"

func TestHeadroomFractionEnv(t *testing.T) {
	t.Setenv("TEST_HEADROOM", "0.5")
	if got := headroomFractionEnv("TEST_HEADROOM", .25); got != .5 {
		t.Fatalf("headroom = %v", got)
	}
	t.Setenv("TEST_HEADROOM", "100")
	if got := headroomFractionEnv("TEST_HEADROOM", .25); got != .25 {
		t.Fatalf("invalid fraction must fall back, got %v", got)
	}
}

func TestReplicaBudgetEnvAcceptsAbsoluteReplicaCount(t *testing.T) {
	t.Setenv("TEST_BUDGET", "100")
	if got := replicaBudgetEnv("TEST_BUDGET", 50); got != 100 {
		t.Fatalf("budget = %v, want 100", got)
	}
	for _, raw := range []string{"0", "-1", "NaN", "Inf", "invalid"} {
		t.Setenv("TEST_BUDGET", raw)
		if got := replicaBudgetEnv("TEST_BUDGET", 50); got != 50 {
			t.Fatalf("budget %q = %v, want fallback", raw, got)
		}
	}
}
