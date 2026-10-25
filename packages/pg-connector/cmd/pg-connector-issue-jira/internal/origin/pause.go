package origin

import (
	"errors"
	"fmt"
	"time"
)

// MaxPause is the ceiling of the doubling: a pause never grows past it before
// jitter is added. A stated delay longer than it is still honoured (it is a
// floor, not a hint).
const MaxPause = time.Hour

// jitterShare is the most jitter added on top of a pause, as a fraction of it.
const jitterShare = 0.2

// ErrPaused is the error an INTERACTIVE call returns while a 429 pause is in
// force. The error returned wraps it and names the end of the pause; a
// BACKGROUND call during the pause returns the governor's paused *Refusal.
var ErrPaused = errors.New("jira origin paused after a rate-limit answer")

// PausedError is the concrete error behind ErrPaused.
type PausedError struct {
	Until  time.Time
	Reason string
}

// Error names the end of the pause.
func (e *PausedError) Error() string {
	return fmt.Sprintf("%s: paused until %s (%s)", ErrPaused, e.Until.UTC().Format(time.RFC3339), e.Reason)
}

// Is makes errors.Is(err, ErrPaused) true.
func (e *PausedError) Is(target error) bool { return target == ErrPaused }

// Paused reports the 429 pause in force: when it ends and why. ok is false
// when none is. The governor's state is the authority.
func (r *Runner) Paused() (until time.Time, reason string, ok bool) {
	bs, found := r.gov.State().Bucket(r.bucket)
	if !found || !bs.Paused {
		return time.Time{}, "", false
	}
	return bs.PausedUntil, bs.PauseReason, true
}

// pauseTracker counts consecutive 429s and sizes the pause of each.
type pauseTracker struct {
	count int           // consecutive 429s so far
	base  time.Duration // the first pause's base (stated delay or default)
}

// reset forgets the run of 429s (the first call that is not a 429).
func (p *pauseTracker) reset() { p.count, p.base = 0, 0 }

// next records one more 429 and returns the pause it earns: the first pause's
// base doubled for each consecutive 429 after the first, capped at MaxPause,
// never shorter than the delay this answer stated, plus jitter of up to a
// fifth of that.
func (p *pauseTracker) next(stated time.Duration, hasStated bool, def time.Duration, jitter func() float64) time.Duration {
	if p.count == 0 {
		p.base = def
		if hasStated {
			p.base = stated
		}
	}
	d := p.base
	for i := 0; i < p.count && d < MaxPause; i++ {
		d *= 2
	}
	p.count++
	d = min(d, MaxPause)
	if hasStated && stated > d {
		d = stated
	}
	j := jitter()
	if j < 0 {
		j = 0
	}
	if j >= 1 {
		j = 0.999999
	}
	return d + time.Duration(float64(d)*jitterShare*j)
}
