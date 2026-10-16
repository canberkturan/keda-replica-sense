package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

type comparisonModel struct {
	value   float64
	origins *[]time.Time
}

func (m comparisonModel) Engine() string { return "test" }
func (m comparisonModel) PredictSamples(samples []domain.Sample, _ Request) (Prediction, error) {
	if m.origins != nil {
		*m.origins = append(*m.origins, samples[len(samples)-1].ObservedAt)
	}
	return Prediction{P50: m.value, P95: m.value}, nil
}

func comparisonFixture() ([]domain.Sample, Request, ComparisonOptions) {
	samples := make([]domain.Sample, 481)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range samples {
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: 10}
	}
	request := Request{Horizon: 10 * time.Minute, SamplingInterval: time.Minute, SeasonalPeriod: time.Hour, Quantile: .97}
	options := ComparisonOptions{After: start.Add(2 * time.Hour), MinDuration: 4 * time.Hour, MinWindows: 12, MaxWindows: 24, Threshold: 5}
	return samples, request, options
}

func TestCompareArtifactsUsesIdenticalIndependentOrigins(t *testing.T) {
	samples, request, options := comparisonFixture()
	var first, second []time.Time
	result, err := CompareArtifacts(context.Background(), map[string]Model{"a": comparisonModel{10, &first}, "b": comparisonModel{12, &second}}, samples, request, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 24 || len(second) != len(first) {
		t.Fatalf("unpaired windows %d/%d", len(first), len(second))
	}
	for i := range first {
		if !first[i].Equal(second[i]) || first[i].Before(options.After) || (i > 0 && first[i].Sub(first[i-1]) < request.Horizon) {
			t.Fatal("unpaired, leaked or overlapping targets")
		}
	}
	if result.Metrics["a"].PinballLossP95 != 0 || math.Abs(result.Metrics["b"].PinballLossP95-.06) > 1e-8 {
		t.Fatalf("scores: %+v", result.Metrics)
	}
	if result.Metrics["b"].ExcessReplicaMinutes != 240 || result.Fingerprint == "" {
		t.Fatal("missing capacity proxy/provenance")
	}
}

func TestCompareArtifactsWithholdsIncompleteOrLeakingEvidence(t *testing.T) {
	for _, name := range []string{"gap", "too_recent", "duration", "nonfinite", "invalid_prediction", "future_tail"} {
		t.Run(name, func(t *testing.T) {
			samples, request, options := comparisonFixture()
			model := comparisonModel{10, nil}
			switch name {
			case "gap":
				var sparse []domain.Sample
				for i, p := range samples {
					if i%15 != 0 {
						sparse = append(sparse, p)
					}
				}
				samples = sparse
			case "too_recent":
				options.After = samples[len(samples)-1].ObservedAt
			case "duration":
				options.MinDuration = 24 * time.Hour
			case "nonfinite":
				samples[3].ObservedValue = math.NaN()
			case "invalid_prediction":
				model.value = math.Inf(1)
			case "future_tail":
				samples = samples[:130]
			}
			if _, err := CompareArtifacts(context.Background(), map[string]Model{"a": model}, samples, request, options); err == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
}

func TestCompareArtifactsDeduplicatesRealSlotsWithoutFabrication(t *testing.T) {
	samples, request, options := comparisonFixture()
	duplicate := samples[100]
	duplicate.ObservedAt = duplicate.ObservedAt.Add(10 * time.Second)
	samples = append(samples[:101], append([]domain.Sample{duplicate}, samples[101:]...)...)
	result, err := CompareArtifacts(context.Background(), map[string]Model{"a": comparisonModel{10, nil}}, samples, request, options)
	if err != nil || result.EligibleWindows != 36 {
		t.Fatalf("duplicate changed evidence windows: %d %v", result.EligibleWindows, err)
	}
}

func TestCompareArtifactsCancelsBoundedWork(t *testing.T) {
	samples, request, options := comparisonFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareArtifacts(ctx, map[string]Model{"a": comparisonModel{10, nil}}, samples, request, options); err == nil {
		t.Fatal("ignored cancellation")
	}
}
