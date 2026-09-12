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
