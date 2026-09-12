package safety

import "testing"

func TestDetectSurgeUsesLeadTimeCap(t *testing.T) {
	got := DetectSurge(SurgeInput{CurrentValue: 100, RecentBaseline: 20, CurrentSlopePerMinute: 170, HistoricalSlopeP95: 25, LeadTimeMinutes: 2, MaxMultiplier: 2})
	if !got.Triggered || got.Demand != 200 {
		t.Fatalf("%#v", got)
	}
}

func TestDetectSurgeFromFlatBaseline(t *testing.T) {
	got := DetectSurge(SurgeInput{CurrentValue: 10, RecentBaseline: 1, CurrentSlopePerMinute: 9, HistoricalSlopeP95: 0, LeadTimeMinutes: 2, MaxMultiplier: 2})
	if !got.Triggered || got.Demand != 20 {
		t.Fatalf("%#v", got)
	}
}
func TestGuardrails(t *testing.T) {
	got, _ := ApplyGuardrails(GuardrailInput{Desired: 15, Current: 2, MaxReplicas: 50, MaxAbsoluteStep: 3, MaxPercentIncrease: 100, ClusterHealthy: true, CapacityAllowed: true})
	if got != 4 {
		t.Fatal(got)
	}
	got, _ = ApplyGuardrails(GuardrailInput{Desired: 15, Current: 2, MaxReplicas: 50, ClusterHealthy: false, CapacityAllowed: true})
	if got != 2 {
		t.Fatal(got)
	}
}
