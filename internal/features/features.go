// Package features builds deterministic model inputs from canonical samples.
package features

import (
	"fmt"
	"math"
	"time"
)

type Point struct {
	ObservedAt time.Time
	Value      float64
}
type Vector map[string]float64

type Provider interface {
	Name() string
	Append([]Point, int, Vector) error
}

type Pipeline struct{ Providers []Provider }

// SchemaV2 identifies the first feature set suitable for feature-rich tree and
// sequence-model experiments. Artifacts must record this exact value.
const SchemaV2 = "demand-calendar-lag-v2"

// DefaultV2 returns providers in their stable column-construction order.
// Location is a workload/business timezone, never the scaler pod timezone.
func DefaultV2(location *time.Location) Pipeline {
	return Pipeline{Providers: []Provider{
		Historical{Lags: []int{1, 2, 5, 10, 15, 30, 60}, RollingWindows: []int{5, 15, 30, 60}},
		Calendar{Location: location},
	}}
}

func (p Pipeline) Build(points []Point, index int) (Vector, error) {
	if index < 0 || index >= len(points) {
		return nil, fmt.Errorf("feature index out of range")
	}
	vector := Vector{}
	for _, provider := range p.Providers {
		if err := provider.Append(points, index, vector); err != nil {
			return nil, fmt.Errorf("%s: %w", provider.Name(), err)
		}
	}
	return vector, nil
}

type Historical struct {
	Lags           []int
	RollingWindows []int
}

func (Historical) Name() string { return "historical" }
func (h Historical) Append(points []Point, index int, vector Vector) error {
	vector["current_value"] = points[index].Value
	for _, lag := range h.Lags {
		if index < lag {
			return fmt.Errorf("need %d prior points", lag)
		}
		vector[fmt.Sprintf("lag_%dm", lag)] = points[index-lag].Value
		vector[fmt.Sprintf("delta_%dm", lag)] = points[index].Value - points[index-lag].Value
		vector[fmt.Sprintf("slope_%dm", lag)] = (points[index].Value - points[index-lag].Value) / float64(lag)
	}
	for _, window := range h.RollingWindows {
		if index+1 < window {
			return fmt.Errorf("need %d points for rolling window", window)
		}
		var sum, max float64
		for i := index - window + 1; i <= index; i++ {
			sum += points[i].Value
			if i == index-window+1 || points[i].Value > max {
				max = points[i].Value
			}
		}
		vector[fmt.Sprintf("rolling_mean_%dm", window)] = sum / float64(window)
		vector[fmt.Sprintf("rolling_max_%dm", window)] = max
		mean := sum / float64(window)
		var squared float64
		for i := index - window + 1; i <= index; i++ {
			difference := points[i].Value - mean
			squared += difference * difference
		}
		vector[fmt.Sprintf("rolling_std_%dm", window)] = math.Sqrt(squared / float64(window))
	}
	return nil
}

// Calendar adds only information known at prediction time. Location should be
// the workload's declared business timezone; nil intentionally means UTC for
// backwards-compatible infrastructure workloads.
type Calendar struct{ Location *time.Location }

func (Calendar) Name() string { return "calendar" }
func (c Calendar) Append(points []Point, index int, vector Vector) error {
	location := time.UTC
	if c.Location != nil {
		location = c.Location
	}
	t := points[index].ObservedAt.In(location)
	vector["minute_of_hour"] = float64(t.Minute())
	vector["hour_of_day"] = float64(t.Hour())
	vector["day_of_week"] = float64(t.Weekday())
	vector["day_of_month"] = float64(t.Day())
	vector["week_of_year"] = float64(isoWeek(t))
	vector["month"] = float64(t.Month())
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		vector["is_weekend"] = 1
	} else {
		vector["is_weekend"] = 0
	}
	_, offset := t.Zone()
	vector["utc_offset_minutes"] = float64(offset / 60)
	hour := float64(t.Hour()) + float64(t.Minute())/60
	day := float64(t.Weekday()) + hour/24
	vector["hour_sin"] = math.Sin(2 * math.Pi * hour / 24)
	vector["hour_cos"] = math.Cos(2 * math.Pi * hour / 24)
	vector["weekday_sin"] = math.Sin(2 * math.Pi * day / 7)
	vector["weekday_cos"] = math.Cos(2 * math.Pi * day / 7)
	month := float64(t.Month() - 1)
	vector["month_sin"] = math.Sin(2 * math.Pi * month / 12)
	vector["month_cos"] = math.Cos(2 * math.Pi * month / 12)
	return nil
}

func isoWeek(t time.Time) int {
	_, week := t.ISOWeek()
	return week
}
