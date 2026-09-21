package dataset

import (
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func TestValidate(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	samples := []domain.Sample{{ObservedAt: start}, {ObservedAt: start.Add(time.Minute)}, {ObservedAt: start.Add(2 * time.Minute)}}
	if err := Validate(samples, QualityPolicy{MinSamples: 3, SamplingInterval: time.Minute, MaxGapIntervals: 2}); err != nil {
		t.Fatal(err)
	}
	samples[2].ObservedAt = start.Add(10 * time.Minute)
	if err := Validate(samples, QualityPolicy{MinSamples: 3, SamplingInterval: time.Minute, MaxGapIntervals: 2}); err == nil {
		t.Fatal("want gap error")
	}
	samples[2].ObservedAt = start.Add(2 * time.Minute)
	samples[1].ObservedValue = math.NaN()
	if err := Validate(samples, QualityPolicy{MinSamples: 3, SamplingInterval: time.Minute, MaxGapIntervals: 2}); err == nil {
		t.Fatal("want non-finite value error")
	}
}

func TestMostRecentContiguousDropsOnlyPreOutageHistory(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	samples := []domain.Sample{
		{ObservedAt: start},
		{ObservedAt: start.Add(time.Minute)},
		{ObservedAt: start.Add(10 * time.Minute)},
		{ObservedAt: start.Add(11 * time.Minute)},
	}
	got := MostRecentContiguous(samples, QualityPolicy{SamplingInterval: time.Minute, MaxGapIntervals: 2})
	if len(got) != 2 || !got[0].ObservedAt.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("MostRecentContiguous() = %#v, want post-gap suffix", got)
	}
}

func TestValidateFresh(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	policy := QualityPolicy{SamplingInterval: time.Minute, MaxGapIntervals: 2}
	if err := ValidateFresh([]domain.Sample{{ObservedAt: now.Add(-2 * time.Minute)}}, now, policy); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFresh([]domain.Sample{{ObservedAt: now.Add(-3 * time.Minute)}}, now, policy); err == nil {
		t.Fatal("want stale-source error")
	}
}
