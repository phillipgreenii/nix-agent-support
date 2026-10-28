package origin

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// ReadingMaxAge is how long the last rate-limit reading is still reported as
// current (the status op, the origin.* gauges). The governor itself uses a
// reading for only one burst window (a second) after it was taken; this is the
// longer reporting age, so a quiet daemon's gauges and the below-floor alert do
// not latch on a reading that is minutes old. It is 5 minutes: the longest
// quiet spell the refresh cadence leaves is one query_ttl_default (5 minutes).
const ReadingMaxAge = 5 * time.Minute

// Latest is the last reading the origin gave and when this daemon took it.
type Latest struct {
	internal.RateLimit
	// At is when the daemon took the reading (the end of the pjira run that
	// carried it).
	At time.Time
}

// Age is how old the reading is at now (never negative).
func (l Latest) Age(now time.Time) time.Duration { return max(now.Sub(l.At), 0) }

// ResetAt is the reset instant the origin STATED (a 429 carries it) and false
// when none was stated or it does not parse as RFC 3339.
func (l Latest) ResetAt() (time.Time, bool) {
	if l.Reset == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, l.Reset)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Readings keeps the latest rate-limit reading, for the status op and the
// origin.* gauges. The governor keeps its own copy for admission. Safe for
// concurrent use.
type Readings struct {
	mu     sync.Mutex
	latest Latest
	has    bool
}

// NewReadings returns an empty holder.
func NewReadings() *Readings { return &Readings{} }

// Record keeps rl, taken at at, as the latest reading. A reading that carries
// neither a limit nor a remaining count nor a near-limit flag (a bare
// Retry-After on a 5xx) is not a budget reading and is not kept.
func (r *Readings) Record(rl internal.RateLimit, at time.Time) {
	if rl.Limit == nil && rl.Remaining == nil && rl.NearLimit == nil {
		return
	}
	r.mu.Lock()
	r.latest, r.has = Latest{RateLimit: rl, At: at}, true
	r.mu.Unlock()
}

// Latest returns the last reading, and false before the first.
func (r *Readings) Latest() (Latest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest, r.has
}

// Current returns the last reading while it is no older than ReadingMaxAge at
// now.
func (r *Readings) Current(now time.Time) (Latest, bool) {
	l, ok := r.Latest()
	if !ok || l.Age(now) > ReadingMaxAge {
		return Latest{}, false
	}
	return l, true
}

// governorReading turns a pjira reading into the governor's, taken at at. It
// returns nil when the reading must not drive admission: it carries no
// remaining count, or its limit is at or below the floor (enforcing a floor the
// tenant's whole window cannot exceed would refuse every request, so the
// reading is exported and shown but never enforced).
//
// The burst window is one second, so the reading is valid until the reset the
// origin stated (a 429) when that is later than at, and otherwise for the
// governor's own window after at. Setting ResetAt always also gives a refusal
// below the floor its RetryAt (the end of that validity), so a scheduler holds
// a class group for that long and no longer.
func governorReading(l Latest, floor int64, window time.Duration) *governor.Reading {
	if l.Remaining == nil || *l.Remaining < 0 {
		return nil
	}
	if floor > 0 && l.Limit != nil && *l.Limit <= floor {
		return nil
	}
	gr := &governor.Reading{Bucket: Bucket, Remaining: *l.Remaining, At: l.At, ResetAt: l.At.Add(window)}
	if l.Limit != nil {
		gr.Limit = *l.Limit
	}
	if stated, ok := l.ResetAt(); ok && stated.After(gr.ResetAt) {
		gr.ResetAt = stated
	}
	return gr
}
