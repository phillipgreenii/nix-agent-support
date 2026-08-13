package eventqueue

import (
	"errors"
	"testing"
	"time"
)

func TestEvent_Validate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	base := Event{ID: "e1", Type: "review-requested"}
	tests := []struct {
		name    string
		mutate  func(Event) Event
		wantErr bool
	}{
		{"valid", func(e Event) Event { return e }, false},
		{"valid with payload", func(e Event) Event { e.Payload = map[string]any{"k": "v"}; return e }, false},
		{"missing id", func(e Event) Event { e.ID = ""; return e }, true},
		{"missing type", func(e Event) Event { e.Type = ""; return e }, true},
		// Both instants are OPTIONAL and DEFAULT, so their absence is the normal
		// case and MUST NOT be an error (INV-EVT-1).
		{"no instants at all", func(e Event) Event { return e }, false},
		{"at only", func(e Event) Event { e.At = now; return e }, false},
		{"expiresAt only", func(e Event) Event { e.ExpiresAt = now; return e }, false},
		// The validation REVERSAL: the DEFAULT event is born expired, so an
		// already-past expiry is the contract's own default behavior and MUST NOT
		// be rejected (DEC-EVENT-1). The duration-valued predecessor rejected a
		// non-positive value here; that rejection has no successor.
		{"expiresAt in the past", func(e Event) Event { e.At = now; e.ExpiresAt = now.Add(-time.Hour); return e }, false},
		{"expiresAt equal to at (born expired)", func(e Event) Event { e.At = now; e.ExpiresAt = now; return e }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mutate(base).Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error not ErrInvalidEvent: %v", err)
			}
		})
	}
}

// Resolve applies the two INV-EVT-1 defaults, in order: `at` falls back to the
// core's ingest-now, then `expiresAt` falls back to the RESOLVED `at`.
func TestEvent_Resolve(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-5 * time.Minute)
	future := now.Add(15 * time.Minute)

	tests := []struct {
		name               string
		in                 Event
		wantAt, wantExpiry time.Time
	}{
		{"neither set: born expired at ingest-now", Event{}, now, now},
		{"at only: expiry follows at, so still born expired", Event{At: stamp}, stamp, stamp},
		{"expiresAt only: at defaults, expiry is honoured", Event{ExpiresAt: future}, now, future},
		{"both set: neither is touched", Event{At: stamp, ExpiresAt: future}, stamp, future},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Resolve(now)
			if !got.At.Equal(tc.wantAt) {
				t.Fatalf("at = %s, want %s", got.At, tc.wantAt)
			}
			if !got.ExpiresAt.Equal(tc.wantExpiry) {
				t.Fatalf("expiresAt = %s, want %s", got.ExpiresAt, tc.wantExpiry)
			}
		})
	}
}

// Resolve is IDEMPOTENT: re-resolving an already-resolved event against a later
// clock leaves both instants alone. That is what lets the queue resolve once at
// ingest and store the result without a second resolution drifting the bound.
func TestEvent_ResolveIsIdempotent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	first := Event{ID: "e", Type: "t"}.Resolve(now)
	second := first.Resolve(now.Add(time.Hour))
	if !second.At.Equal(first.At) || !second.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("re-resolve moved the bound: %s/%s -> %s/%s",
			first.At, first.ExpiresAt, second.At, second.ExpiresAt)
	}
}

// Expired is the single stateless comparison INV-EVT-4 makes at attempt time. The
// boundary is INCLUSIVE of the expiry instant: an event is expired AT
// `expiresAt`, which is what makes the born-expired default (expiresAt == at ==
// ingest-now) actually born expired rather than alive for one instant.
func TestEvent_Expired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		evt  Event
		want bool
	}{
		{"expiry in the future", Event{ExpiresAt: now.Add(time.Nanosecond)}, false},
		{"expiry exactly now", Event{ExpiresAt: now}, true},
		{"expiry in the past", Event{ExpiresAt: now.Add(-time.Nanosecond)}, true},
		{"born expired (resolved from a bare event)", Event{}.Resolve(now), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.evt.Expired(now); got != tc.want {
				t.Fatalf("Expired(%s) = %v, want %v", now, got, tc.want)
			}
		})
	}
}
