package features

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPipeline(t *testing.T) {
	points := []Point{{time.Now(), 1}, {time.Now(), 2}, {time.Now(), 3}}
	v, err := Pipeline{Providers: []Provider{Historical{Lags: []int{1}, RollingWindows: []int{2}}, Calendar{}}}.Build(points, 2)
	if err != nil || v["lag_1m"] != 2 || v["delta_1m"] != 1 || v["slope_1m"] != 1 || v["rolling_mean_2m"] != 2.5 || v["rolling_std_2m"] != 0.5 {
		t.Fatalf("%v %#v", err, v)
	}
}

func TestCalendarUsesDeclaredBusinessTimezoneAndCyclicalValues(t *testing.T) {
	location, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal(err)
	}
	// Friday 21:30 UTC is Saturday 00:30 in the declared business timezone.
	point := Point{ObservedAt: time.Date(2026, 9, 4, 21, 30, 0, 0, time.UTC)}
	vector := Vector{}
	err = (Calendar{Location: location}).Append([]Point{point}, 0, vector)
	if err != nil {
		t.Fatal(err)
	}
	if vector["hour_of_day"] != 0 || vector["minute_of_hour"] != 30 || vector["day_of_week"] != float64(time.Saturday) || vector["is_weekend"] != 1 {
		t.Fatalf("unexpected local calendar vector: %#v", vector)
	}
	if vector["utc_offset_minutes"] != 180 || vector["hour_sin"] <= 0 || vector["hour_cos"] <= 0 {
		t.Fatalf("unexpected timezone/cycle values: %#v", vector)
	}
}

func TestDefaultV3HasStableFeatureContract(t *testing.T) {
	points := make([]Point, 61)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range points {
		points[i] = Point{ObservedAt: start.Add(time.Duration(i) * time.Minute), Value: float64(i)}
	}
	pipeline := DefaultV3(time.UTC)
	vector, err := pipeline.Build(points, 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lag_60m", "rolling_std_60m", "acceleration_1m", "acceleration_2m", "slope_ratio_1m_5m", "slope_ratio_2m_10m", "day_of_month", "weekday_sin", "month_cos", "utc_offset_minutes"} {
		if _, ok := vector[key]; !ok {
			t.Fatalf("missing %s in %#v", key, vector)
		}
	}
	names, err := pipeline.Names(points, 60)
	if err != nil {
		t.Fatal(err)
	}
	namesAgain, err := pipeline.Names(points, 60)
	if err != nil || !reflect.DeepEqual(names, namesAgain) {
		t.Fatalf("feature names are not deterministic: %v %v", names, namesAgain)
	}
}

func TestAccelerationFeaturesAreCausalFiniteAndDiscriminateTrends(t *testing.T) {
	tests := []struct {
		name       string
		value      func(int) float64
		wantAcc1   float64
		wantRatio1 float64
	}{
		{name: "constant", value: func(int) float64 { return 10 }, wantAcc1: 0, wantRatio1: 0},
		{name: "linear", value: func(i int) float64 { return float64(i) }, wantAcc1: 0, wantRatio1: 1},
		{name: "accelerating", value: func(i int) float64 { return float64(i * i) }, wantAcc1: 1, wantRatio1: 119.0 / 115.0},
		{name: "declining", value: func(i int) float64 { return -float64(i) }, wantAcc1: 0, wantRatio1: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			points := make([]Point, 61)
			for i := range points {
				points[i] = Point{ObservedAt: time.Unix(int64(i*60), 0).UTC(), Value: test.value(i)}
			}
			vector, err := DefaultV3(time.UTC).Build(points, 60)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(vector["acceleration_1m"]-test.wantAcc1) > 1e-9 || math.Abs(vector["slope_ratio_1m_5m"]-test.wantRatio1) > 1e-9 {
				t.Fatalf("unexpected acceleration features: %#v", vector)
			}
			for name, value := range vector {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					t.Fatalf("%s is non-finite: %v", name, value)
				}
			}
		})
	}
}

func TestSafeSlopeRatioHandlesZeroDenominator(t *testing.T) {
	if got := safeSlopeRatio(42, 0); got != 0 {
		t.Fatalf("safeSlopeRatio = %v, want 0", got)
	}
}
