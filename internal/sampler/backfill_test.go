package sampler

import (
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
)

func TestHasSufficientHistory(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	if !hasSufficientHistory(workloadstore.SampleCoverage{OldestObservedAt: start, NewestObservedAt: end.Add(-time.Minute), Count: 43199}, start, end, time.Minute) {
		t.Fatal("complete coverage should be sufficient")
	}
	if hasSufficientHistory(workloadstore.SampleCoverage{}, start, end, time.Minute) {
		t.Fatal("empty coverage should need backfill")
	}
	if hasSufficientHistory(workloadstore.SampleCoverage{OldestObservedAt: start, NewestObservedAt: end, Count: 100}, start, end, time.Minute) {
		t.Fatal("endpoint coverage with a large internal gap must need backfill")
	}
}
