package dataset

import (
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// TrainingSegment chooses the newest sufficiently long real contiguous
// segment. Backfill/live duplicates are deduplicated; outages are never joined
// or interpolated. Its true end must be persisted as the artifact fit boundary.
func TrainingSegment(observed []domain.Sample, interval time.Duration, minimum int) ([]domain.Sample, error) {
	if interval <= 0 || minimum < 2 {
		return nil, fmt.Errorf("invalid training segment policy")
	}
	var points []domain.Sample
	for i, p := range observed {
		if math.IsNaN(p.ObservedValue) || math.IsInf(p.ObservedValue, 0) || p.ObservedValue < 0 || (i > 0 && !p.ObservedAt.After(observed[i-1].ObservedAt)) {
			return nil, fmt.Errorf("invalid training observation")
		}
		if len(points) > 0 && points[len(points)-1].ObservedAt.Truncate(interval).Equal(p.ObservedAt.Truncate(interval)) {
			points[len(points)-1] = p
		} else {
			points = append(points, p)
		}
	}
	var selected []domain.Sample
	start := 0
	for i := 1; i <= len(points); i++ {
		if i < len(points) && points[i].ObservedAt.Truncate(interval).Sub(points[i-1].ObservedAt.Truncate(interval)) == interval {
			continue
		}
		if i-start >= minimum {
			selected = points[start:i]
		}
		start = i
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("insufficient contiguous real training history: need %d samples", minimum)
	}
	return selected, nil
}
