package safety

import "testing"

func TestDetectSurgeUsesLeadTimeCap(t *testing.T) {
	got := DetectSurge(SurgeInput{CurrentValue: 100, RecentBaseline: 20, CurrentSlopePerMinute: 170, HistoricalSlopeP95: 25, LeadTimeMinutes: 2}, SurgePolicy{SlopeMultiplier: 3, BaselineMultiplier: 1.5, MaxMultiplier: 1.5})
	if !got.Triggered || got.Demand != 150 {
		t.Fatalf("%#v", got)
	}
}

func TestDetectSurgeFromFlatBaseline(t *testing.T) {
	got := DetectSurge(SurgeInput{CurrentValue: 10, RecentBaseline: 1, CurrentSlopePerMinute: 9, HistoricalSlopeP95: 0, LeadTimeMinutes: 2}, DefaultSurgePolicy())
	if !got.Triggered || got.Demand != 15 {
		t.Fatalf("%#v", got)
	}
}

func TestDetectSurgeRejectsSingleSignal(t *testing.T) {
	policy := DefaultSurgePolicy()
	for _, input := range []SurgeInput{
		{CurrentValue: 20, RecentBaseline: 10, CurrentSlopePerMinute: 10, HistoricalSlopeP95: 10, LeadTimeMinutes: 2},
		{CurrentValue: 11, RecentBaseline: 10, CurrentSlopePerMinute: 50, HistoricalSlopeP95: 10, LeadTimeMinutes: 2},
	} {
		if got := DetectSurge(input, policy); got.Triggered {
			t.Fatalf("single signal triggered surge: %#v", got)
		}
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
