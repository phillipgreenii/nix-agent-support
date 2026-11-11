package main

import (
	"slices"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/metrics"
)

// The metrics Emitter answers the discover package's level pull observers; the
// assertion lives here because metrics cannot import discover.
var (
	_ discover.LevelPullObserver       = (*metrics.Emitter)(nil)
	_ discover.LevelPullTimingObserver = (*metrics.Emitter)(nil)
)

// levelSamples converts the projection's per-resource rows into the metric
// layer's samples, one per registry resource. A resource with no level in force
// reads as its scale's unknown value.
func levelSamples(p *core.LevelProjection, reg levels.Registry, now time.Time) []metrics.LevelSample {
	details := p.Details()
	out := make([]metrics.LevelSample, 0, len(details))
	for _, d := range details {
		scale := reg[d.Resource]
		s := metrics.LevelSample{
			Resource: d.Resource, HasLevel: d.HasLevel, Level: scale.Unknown, ScaleSize: len(scale.Levels),
			Stale: d.Stale, OverrideActive: d.Override != nil,
		}
		if d.HasLevel {
			s.Level, s.Source, s.Owner, s.ObservedAt, s.Age = d.Level, d.Source, d.Owner, d.ObservedAt, d.Age
			if !d.ExpiresAt.IsZero() {
				s.HasLease, s.LeaseRemaining = true, d.ExpiresAt.Sub(now)
			}
			if d.Reading != nil {
				s.Reading = &metrics.LevelReading{Value: d.Reading.Value, Unit: d.Reading.Unit}
			}
		}
		s.Index = max(slices.Index(scale.Levels, s.Level), 0)
		out = append(out, s)
	}
	return out
}
