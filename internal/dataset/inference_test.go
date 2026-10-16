package dataset

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

func inferenceObservations(count int, missing map[int]bool) []domain.Sample {
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	var samples []domain.Sample
	for index := 0; index < count; index++ {
		if !missing[index] {
			samples = append(samples, domain.Sample{ObservedAt: start.Add(time.Duration(index) * time.Minute), ObservedValue: 100 + float64(index)/10})
		}
	}
	return samples
}

func TestInferenceHistoryBridgesShortGapWithoutMutatingObservedData(t *testing.T) {
	observed := inferenceObservations(70, map[int]bool{64: true, 65: true})
	before := append([]domain.Sample(nil), observed...)
	got, err := PrepareInferenceHistory(observed, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Samples) != 61 || got.EstimatedCount != 2 || got.UsedContiguousSuffix {
		t.Fatalf("inference history = %#v", got)
	}
	for index, estimated := range got.Estimated {
		minute := int(got.Samples[index].ObservedAt.Sub(before[0].ObservedAt) / time.Minute)
		if estimated != (minute == 64 || minute == 65) {
			t.Fatalf("point at minute %d has incorrect provenance", minute)
		}
		want := 100 + float64(minute)/10
		if math.Abs(got.Samples[index].ObservedValue-want) > 1e-9 {
			t.Fatalf("point at minute %d = %v, want %v", minute, got.Samples[index].ObservedValue, want)
		}
	}
	if !reflect.DeepEqual(before, observed) {
		t.Fatal("inference interpolation modified observed samples")
	}
	if got.Estimated[len(got.Estimated)-1] || !got.Samples[len(got.Samples)-1].ObservedAt.Equal(observed[len(observed)-1].ObservedAt) {
		t.Fatal("last inference point must be a real observation, not extrapolation")
	}
}

func TestInferenceHistoryRejectsInsufficientConfirmationAndDenseEstimates(t *testing.T) {
	for _, test := range []struct {
		name    string
		count   int
		missing map[int]bool
	}{
		{"only one real post-gap point", 67, map[int]bool{64: true, 65: true}},
		{"dense recent missing points", 70, map[int]bool{60: true, 61: true, 62: true, 63: true}},
		{"several small recent gaps", 100, map[int]bool{60: true, 70: true, 80: true, 90: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PrepareInferenceHistory(inferenceObservations(test.count, test.missing), InterpolationPolicy{SamplingInterval: time.Minute}); err == nil {
				t.Fatal("unsafe interpolation must be withheld")
			}
		})
	}
}

func TestInferenceHistoryDropsLongOrUnreliableGapRatherThanFabricatingIt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]domain.Sample)
		input  []domain.Sample
	}{
		{"long outage", func([]domain.Sample) {}, inferenceObservations(80, map[int]bool{60: true, 61: true, 62: true, 63: true, 64: true, 65: true, 66: true})},
		{"large anchor change", func(samples []domain.Sample) {
			for index := range samples {
				if index >= 60 {
					samples[index].ObservedValue = 1000
				}
			}
		}, inferenceObservations(70, map[int]bool{60: true, 61: true})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.mutate(test.input)
			got, err := PrepareInferenceHistory(test.input, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61})
			if err != nil || !got.UsedContiguousSuffix || got.EstimatedCount != 0 || len(got.Samples) >= 61 {
				t.Fatalf("must return an insufficient real suffix, not bridge: %#v, %v", got, err)
			}
		})
	}
}

func TestInferenceHistoryRejectsInvalidObservations(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]domain.Sample)
	}{
		{"NaN", func(samples []domain.Sample) { samples[10].ObservedValue = math.NaN() }},
		{"Inf", func(samples []domain.Sample) { samples[10].ObservedValue = math.Inf(1) }},
		{"negative", func(samples []domain.Sample) { samples[10].ObservedValue = -1 }},
		{"duplicate timestamp", func(samples []domain.Sample) { samples[10].ObservedAt = samples[9].ObservedAt }},
		{"unordered", func(samples []domain.Sample) { samples[10].ObservedAt = samples[8].ObservedAt }},
	} {
		t.Run(test.name, func(t *testing.T) {
			samples := inferenceObservations(70, nil)
			test.mutate(samples)
			if _, err := PrepareInferenceHistory(samples, InterpolationPolicy{SamplingInterval: time.Minute}); err == nil {
				t.Fatal("unreliable data must not be accepted")
			}
		})
	}
}

func TestInferenceHistoryAllowsSamplerJitterAndIgnoresUnconsumedOldHistory(t *testing.T) {
	observed := inferenceObservations(100, map[int]bool{85: true, 86: true})
	observed[0].ObservedValue = math.NaN()
	for index := range observed {
		observed[index].ObservedAt = observed[index].ObservedAt.Add(time.Duration(index%2) * time.Second)
	}
	got, err := PrepareInferenceHistory(observed, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61})
	if err != nil || got.EstimatedCount != 2 || len(got.Samples) != 61 {
		t.Fatalf("bounded jitter/old unused history must not prevent recovery: %#v, %v", got, err)
	}
}

func TestInferenceHistoryEstimatesAgeOutAndSurgeEvidenceRemainsReal(t *testing.T) {
	observed := inferenceObservations(140, map[int]bool{60: true, 61: true})
	got, err := PrepareInferenceHistory(observed, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61})
	if err != nil || got.EstimatedCount != 0 {
		t.Fatalf("expired estimates must not keep recovery mode active: %#v, %v", got, err)
	}
	real := RealContiguousSuffix(observed, time.Minute)
	if len(real) != 78 || !real[0].ObservedAt.Equal(observed[60].ObservedAt) {
		t.Fatalf("surge must use only actual post-gap observations: %#v", real)
	}
}

func TestInferenceRecoveryRequiresDistinctRealSamplingSlots(t *testing.T) {
	observed := inferenceObservations(67, map[int]bool{64: true, 65: true})
	duplicateSlot := observed[len(observed)-1]
	duplicateSlot.ObservedAt = duplicateSlot.ObservedAt.Add(10 * time.Second)
	observed = append(observed, duplicateSlot)
	if _, err := PrepareInferenceHistory(observed, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61}); err == nil {
		t.Fatal("two database rows in the same slot must not fake two recovery confirmations")
	}
}

func TestBackfillLiveOverlapDoesNotShrinkModelLookback(t *testing.T) {
	var overlapped []domain.Sample
	for _, sample := range inferenceObservations(70, map[int]bool{64: true, 65: true}) {
		overlapped = append(overlapped, sample)
		sample.ObservedAt = sample.ObservedAt.Add(10 * time.Second)
		overlapped = append(overlapped, sample)
	}
	got, err := PrepareInferenceHistory(overlapped, InterpolationPolicy{SamplingInterval: time.Minute, Lookback: 61})
	if err != nil || len(got.Samples) != 61 || got.EstimatedCount != 2 {
		t.Fatalf("overlapping real writes must not fake a shorter warm-up: inputs=%d estimated=%d err=%v", len(got.Samples), got.EstimatedCount, err)
	}
	if got := RealContiguousSuffix(overlapped, time.Minute); len(got) != 4 {
		t.Fatalf("surge confirmations must use distinct real slots, got %d", len(got))
	}
}
