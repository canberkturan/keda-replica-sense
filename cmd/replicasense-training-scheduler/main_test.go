package main

import "testing"

func TestTrainerResourcesUsesSafeDefaults(t *testing.T) {
	resources, err := trainerResources(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("CPU request = %q, want 500m", got)
	}
	if got := resources.Limits.Memory().String(); got != "2Gi" {
		t.Fatalf("memory limit = %q, want 2Gi", got)
	}
}

func TestTrainerResourcesRejectsInvalidQuantity(t *testing.T) {
	_, err := trainerResources(func(name string) string {
		if name == "REPLICASENSE_TRAINER_LIMIT_MEMORY" {
			return "not-a-quantity"
		}
		return ""
	})
	if err == nil {
		t.Fatal("invalid trainer quantity must fail startup")
	}
}

func TestPositiveInt64OrDefault(t *testing.T) {
	if value, err := positiveInt64OrDefault("", 3600); err != nil || value != 3600 {
		t.Fatalf("default = %d, %v", value, err)
	}
	if _, err := positiveInt64OrDefault("0", 3600); err == nil {
		t.Fatal("zero active deadline must be rejected")
	}
}
