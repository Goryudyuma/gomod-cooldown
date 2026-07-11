package availability

import (
	"context"
	"testing"
	"time"
)

func TestGoIndexSource(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := GoIndexSource{Cutoff: cutoff, Recent: map[string]time.Time{Key("example.com/m", "v1.0.0"): cutoff.Add(time.Hour)}}
	a, err := s.AvailableAt(context.Background(), "example.com/m", "v1.0.0", time.Time{})
	if err != nil || a.FirstCached == nil || !a.AvailableAt.Equal(cutoff.Add(time.Hour)) {
		t.Fatal(a, err)
	}
	a, err = s.AvailableAt(context.Background(), "example.com/m", "v0.9.0", time.Time{})
	if err != nil || !a.AvailableAt.Equal(cutoff) || a.FirstCached != nil {
		t.Fatal(a, err)
	}
}
