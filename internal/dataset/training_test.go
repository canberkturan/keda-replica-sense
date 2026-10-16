package dataset

import (
	"github.com/canberkturan/keda-replica-sense/internal/domain"
	"testing"
	"time"
)

func TestTrainingSegmentNeverJoinsOutagesOrCountsDuplicateSlots(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var samples []domain.Sample
	for i := 0; i < 100; i++ {
		samples = append(samples, domain.Sample{ObservedAt: start.Add(time.Duration(i) * time.Minute), ObservedValue: 10})
	}
	for i := 0; i < 10; i++ {
		samples = append(samples, domain.Sample{ObservedAt: start.Add(time.Duration(200+i) * time.Minute), ObservedValue: 20})
	}
	segment, err := TrainingSegment(samples, time.Minute, 60)
	if err != nil || len(segment) != 100 || segment[len(segment)-1].ObservedValue != 10 {
		t.Fatalf("must retain real complete segment: %d %v", len(segment), err)
	}
	if _, err := TrainingSegment(samples, time.Minute, 110); err == nil {
		t.Fatal("joined source outage")
	}
	var duplicates []domain.Sample
	for _, p := range samples[:50] {
		duplicates = append(duplicates, p)
		p.ObservedAt = p.ObservedAt.Add(time.Second)
		duplicates = append(duplicates, p)
	}
	if _, err := TrainingSegment(duplicates, time.Minute, 60); err == nil {
		t.Fatal("duplicate slots faked coverage")
	}
}
