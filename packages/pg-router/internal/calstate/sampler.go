package calstate

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Reading is one sample of the two clocks. Wall is the wall clock (it counts
// host sleep); Mono is a monotonic offset from an arbitrary base (it does NOT
// count host sleep).
//
// Assumption (recorded, not yet confirmed from Go's runtime source for darwin
// and linux; the sampler's tests fabricate readings rather than prove it): the
// Go monotonic clock does not advance while the host sleeps, so a wall gap
// larger than the monotonic gap by more than one poll interval is a sleep. A
// forward wall-clock correction looks the same and behaves as a wake.
type Reading struct {
	Wall time.Time
	Mono time.Duration
}

// Span is a recorded host sleep, [Start, End].
type Span struct{ Start, End time.Time }

// RealSource returns the production reading source.
func RealSource() func() Reading {
	base := time.Now()
	return func() Reading {
		n := time.Now()
		return Reading{Wall: n.Round(0), Mono: n.Sub(base)}
	}
}

// Sampler detects host sleep independently of the producer loop: each reading
// is compared with the previous one, and a wall advance exceeding the monotonic
// advance by more than the poll interval records a sleep span. It never waits on
// a producer pass, so a slow pass or load back-off cannot stretch a span.
type Sampler struct {
	poll time.Duration
	src  func() Reading

	mu      sync.Mutex
	last    Reading
	hasLast bool
	spans   []Span
}

// maxSpans bounds memory; the oldest spans are dropped first.
const maxSpans = 256

// NewSampler builds a sampler with the given poll interval and reading source.
func NewSampler(poll time.Duration, src func() Reading) *Sampler {
	if poll <= 0 {
		poll = 10 * time.Second // the router's default poll interval
	}
	return &Sampler{poll: poll, src: src}
}

// Observe feeds one reading (tests inject fabricated pairs here).
func (s *Sampler) Observe(r Reading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasLast {
		if r.Mono <= s.last.Mono {
			return // out of order or duplicate
		}
		wall := r.Wall.Sub(s.last.Wall)
		mono := r.Mono - s.last.Mono
		if wall-mono > s.poll {
			s.spans = append(s.spans, Span{Start: s.last.Wall, End: r.Wall})
			if len(s.spans) > maxSpans {
				s.spans = s.spans[len(s.spans)-maxSpans:]
			}
		}
	}
	s.last, s.hasLast = r, true
}

// Sample takes one reading from the source.
func (s *Sampler) Sample() { s.Observe(s.src()) }

// Run samples on its own ticker (period = poll interval) until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	s.Sample()
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sample()
		}
	}
}

// AsleepAt reports whether t lies inside a recorded sleep span.
func (s *Sampler) AsleepAt(t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.spans {
		if !t.Before(sp.Start) && !t.After(sp.End) {
			return true
		}
	}
	return false
}

// Drop forgets spans that ended before cutoff (the oldest remembered
// last_slot).
func (s *Sampler) Drop(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := sort.Search(len(s.spans), func(i int) bool { return !s.spans[i].End.Before(cutoff) })
	s.spans = s.spans[i:]
}

// Spans returns a copy of the recorded spans.
func (s *Sampler) Spans() []Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Span(nil), s.spans...)
}
