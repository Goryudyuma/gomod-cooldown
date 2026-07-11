// Package availability determines whether a module version is old enough to
// participate in version discovery.
package availability

import (
	"context"
	"fmt"
	"time"
)

// Availability describes the timestamps used for a decision. FirstCached is
// nil when the complete recent index snapshot proves it predates the cutoff.
type Availability struct {
	CommitTime  time.Time
	FirstCached *time.Time
	AvailableAt time.Time
}

// Source supplies the availability time for a version.
type Source interface {
	AvailableAt(ctx context.Context, modulePath, version string, commitTime time.Time) (Availability, error)
}

// CommitTimeSource uses only the commit timestamp reported by .info.
type CommitTimeSource struct{}

// AvailableAt returns the supplied commit time as the availability time.
func (CommitTimeSource) AvailableAt(_ context.Context, _ string, _ string, commit time.Time) (Availability, error) {
	return Availability{CommitTime: commit, AvailableAt: commit}, nil
}

// GoIndexSource uses first-cached timestamps supplied by a complete
// index.golang.org snapshot. A missing version is known to predate Cutoff but
// has no exact timestamp, so it is represented by Cutoff for an inclusive
// cooldown decision.
type GoIndexSource struct {
	Recent map[string]time.Time
	Cutoff time.Time
}

// AvailableAt returns the first-cached availability time from the snapshot.
func (s GoIndexSource) AvailableAt(_ context.Context, path, version string, _ time.Time) (Availability, error) {
	if cached, ok := s.Recent[Key(path, version)]; ok {
		return Availability{FirstCached: &cached, AvailableAt: cached}, nil
	}
	if s.Cutoff.IsZero() {
		return Availability{}, fmt.Errorf("first-cached time for %s@%s is not in the snapshot", path, version)
	}
	return Availability{AvailableAt: s.Cutoff}, nil
}

// CombinedSource combines .info commit time with a complete index snapshot.
// The snapshot only contains records at or after its cutoff.
type CombinedSource struct {
	Recent map[string]time.Time
}

// Key returns an unambiguous map key for a module path and version.
func Key(path, version string) string { return path + "\x00" + version }

// AvailableAt returns the later of the commit and first-cached timestamps.
func (s CombinedSource) AvailableAt(_ context.Context, path, version string, commit time.Time) (Availability, error) {
	a := Availability{CommitTime: commit, AvailableAt: commit}
	if cached, ok := s.Recent[Key(path, version)]; ok {
		a.FirstCached = &cached
		if cached.After(a.AvailableAt) {
			a.AvailableAt = cached
		}
	}
	return a, nil
}
