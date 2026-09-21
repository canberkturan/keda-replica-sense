package forecaster

import (
	"context"
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"github.com/canberkturan/keda-replica-sense/internal/forecast"
	"github.com/canberkturan/keda-replica-sense/internal/safety"
	"github.com/canberkturan/keda-replica-sense/internal/workloadstore"
	"testing"
	"time"
)

type samples struct{ values []domain.Sample }

func (s samples) ListSamplesForWindow(context.Context, workloadstore.SamplingWorkload, time.Time, time.Time) ([]domain.Sample, error) {
	return s.values, nil
}

type snapshots struct{ s domain.ForecastSnapshot }

func (s *snapshots) SaveSnapshot(_ context.Context, v domain.ForecastSnapshot) error {
	s.s = v
	return nil
}
func (s *snapshots) LatestSnapshot(context.Context, forecast.Lookup) (*domain.ForecastSnapshot, error) {
	return &s.s, nil
}

type capturingModel struct{ received []domain.Sample }

func (m *capturingModel) Engine() string { return "test" }
func (m *capturingModel) PredictSamples(samples []domain.Sample, _ forecast.Request) (forecast.Prediction, error) {
	m.received = append([]domain.Sample(nil), samples...)
	return forecast.Prediction{P50: 1, P95: 1}, nil
}

type staticModels struct{ model forecast.Model }

func (m staticModels) ModelFor(workloadstore.SamplingWorkload) (forecast.Model, bool) {
	return m.model, true
}
func TestForecast(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{{ObservedAt: start, ObservedValue: 1}, {ObservedAt: start.Add(time.Minute), ObservedValue: 2}, {ObservedAt: start.Add(2 * time.Minute), ObservedValue: 3}, {ObservedAt: start.Add(3 * time.Minute), ObservedValue: 4}}
	out := &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 3}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 4 * time.Minute, Horizon: time.Minute}}}
	got, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 4}.Forecast(context.Background(), w, start.Add(4*time.Minute))
	if err != nil || got.SafeDemand != 3 {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestForecastUsesContinuousSuffixAfterSourceOutage(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{
		{ObservedAt: start, ObservedValue: 1},
		{ObservedAt: start.Add(time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(2 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(10 * time.Minute), ObservedValue: 2},
		{ObservedAt: start.Add(11 * time.Minute), ObservedValue: 2},
		{ObservedAt: start.Add(12 * time.Minute), ObservedValue: 2},
		{ObservedAt: start.Add(13 * time.Minute), ObservedValue: 2},
	}
	model, out := &capturingModel{}, &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 3}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 14 * time.Minute, Horizon: time.Minute}}}
	got, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 4, Models: staticModels{model: model}}.Forecast(context.Background(), w, start.Add(14*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(model.received) != 4 || !model.received[0].ObservedAt.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("model received %#v, want only post-outage observations", model.received)
	}
	if got.SafetyReason != "baseline_replicas;recovered_after_source_gap;rate_limited" {
		t.Fatalf("safety reason = %q", got.SafetyReason)
	}
}

func TestForecastRejectsStaleSourceData(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{{ObservedAt: start, ObservedValue: 1}, {ObservedAt: start.Add(time.Minute), ObservedValue: 1}, {ObservedAt: start.Add(2 * time.Minute), ObservedValue: 1}, {ObservedAt: start.Add(3 * time.Minute), ObservedValue: 1}}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 3}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 4 * time.Minute, Horizon: time.Minute}}}
	_, err := Service{Samples: samples{vals}, Snapshots: &snapshots{}, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 4}.Forecast(context.Background(), w, start.Add(6*time.Minute))
	if err == nil {
		t.Fatal("want stale-source error")
	}
}

func TestForecastFreezesPredictiveScaleUpWhenCapacityDenied(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{{ObservedAt: start, ObservedValue: 1}, {ObservedAt: start.Add(time.Minute), ObservedValue: 2}, {ObservedAt: start.Add(2 * time.Minute), ObservedValue: 3}, {ObservedAt: start.Add(3 * time.Minute), ObservedValue: 4}}
	out := &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 10}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 4 * time.Minute, Horizon: time.Minute}}}
	got, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 4, CurrentReplicas: func(context.Context, workloadstore.SamplingWorkload) (float64, error) { return 2, nil }, PredictiveAllowed: func(context.Context, workloadstore.SamplingWorkload, float64, float64) bool { return false }}.Forecast(context.Background(), w, start.Add(4*time.Minute))
	if err != nil || got.SafeDemand != 2 || got.SafetyReason != "baseline_replicas;capacity_unavailable" {
		t.Fatalf("Forecast() = %#v, %v", got, err)
	}
}

func TestForecastPersistsStartupAwareDecisionHorizon(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{{ObservedAt: start, ObservedValue: 1}, {ObservedAt: start.Add(time.Minute), ObservedValue: 2}, {ObservedAt: start.Add(2 * time.Minute), ObservedValue: 3}, {ObservedAt: start.Add(3 * time.Minute), ObservedValue: 4}}
	out := &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 10}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 4 * time.Minute, Horizon: time.Minute, StartupLatency: time.Minute, SafetyBuffer: time.Minute}}}
	_, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 4}.Forecast(context.Background(), w, start.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.s.ForecastHorizon, 3*time.Minute; got != want {
		t.Fatalf("stored horizon = %s, want %s", got, want)
	}
}

func TestForecastPersistsSurgeProjectionAsOperationalP95(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{
		{ObservedAt: start, ObservedValue: 1},
		{ObservedAt: start.Add(time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(2 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(3 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(4 * time.Minute), ObservedValue: 2},
		{ObservedAt: start.Add(5 * time.Minute), ObservedValue: 6},
		{ObservedAt: start.Add(6 * time.Minute), ObservedValue: 12},
	}
	out := &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 30}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 7 * time.Minute, Horizon: time.Minute}}}
	got, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 7, MaxAbsoluteStep: 30}.Forecast(context.Background(), w, start.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.ForecastP95 != 6 || got.SurgeDemand != 18 || got.SafetyReason != "sustained_abnormal_slope_and_baseline;rate_limited" {
		t.Fatalf("Forecast() = %#v", got)
	}
}

func TestForecastCanDisableSurgeDetectionForModelEvaluation(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vals := []domain.Sample{
		{ObservedAt: start, ObservedValue: 1},
		{ObservedAt: start.Add(time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(2 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(3 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(4 * time.Minute), ObservedValue: 1},
		{ObservedAt: start.Add(5 * time.Minute), ObservedValue: 10},
	}
	out := &snapshots{}
	w := workloadstore.SamplingWorkload{ID: "id", Spec: domain.WorkloadSpec{Key: domain.WorkloadKey{ClusterID: "c"}, Bounds: domain.ReplicaBounds{Max: 30}, Source: domain.PrometheusSource{Threshold: 1}, SourceFingerprint: "x", Forecast: domain.ForecastConfig{SamplingInterval: time.Minute, TrainingWindow: 6 * time.Minute, Horizon: time.Minute}}}
	got, err := Service{Samples: samples{vals}, Snapshots: out, SeasonalPeriod: 2 * time.Minute, MinimumSamples: 6, MaxAbsoluteStep: 30, DisableSurgeDetection: true}.Forecast(context.Background(), w, start.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.ForecastP95 != 1 || got.SurgeDemand != 0 {
		t.Fatalf("Forecast() = %#v", got)
	}
}

func TestDetectSurgeUsesWorkloadAwareLeadTime(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []float64{1, 1, 1, 1, 2, 6, 12}
	samples := make([]domain.Sample, len(values))
	for i, value := range values {
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: value}
	}
	tests := []struct {
		name string
		lead time.Duration
		want float64
	}{
		{name: "30 second startup", lead: 30 * time.Second, want: 15},
		{name: "two minute startup", lead: 2 * time.Minute, want: 18},
		{name: "five minute startup", lead: 5 * time.Minute, want: 18},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := detectSurge(samples, time.Minute, test.lead, safety.DefaultSurgePolicy())
			if !got.Triggered || got.Demand != test.want {
				t.Fatalf("surge = %#v, want %v", got, test.want)
			}
		})
	}
}

func TestDetectSurgeRequiresTwoConsecutiveCandidates(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []float64{1, 1, 1, 1, 1, 1, 5}
	samples := make([]domain.Sample, len(values))
	for i, value := range values {
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: value}
	}
	if got := detectSurge(samples, time.Minute, 2*time.Minute, safety.DefaultSurgePolicy()); got.Triggered {
		t.Fatalf("single observation triggered surge: %#v", got)
	}
}

func TestDetectSurgeHonorsConfiguredConfirmationCount(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []float64{1, 1, 1, 1, 1, 5}
	samples := make([]domain.Sample, len(values))
	for i, value := range values {
		samples[i] = domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: value}
	}
	policy := safety.DefaultSurgePolicy()
	policy.ConfirmationSamples = 1
	if got := detectSurge(samples, time.Minute, 2*time.Minute, policy); !got.Triggered {
		t.Fatalf("one configured confirmation did not trigger: %#v", got)
	}
}

func TestSurgeLeadTimeUsesStartupLatencyAndSafetyBuffer(t *testing.T) {
	if got := surgeLeadTime(domain.ForecastConfig{StartupLatency: 30 * time.Second, SafetyBuffer: 15 * time.Second}); got != 45*time.Second {
		t.Fatalf("lead time = %s", got)
	}
	if got := surgeLeadTime(domain.ForecastConfig{}); got != defaultSurgeLeadTime {
		t.Fatalf("fallback lead time = %s", got)
	}
}
