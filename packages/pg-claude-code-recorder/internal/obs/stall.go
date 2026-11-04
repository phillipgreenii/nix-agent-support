package obs

import "time"

// The stall bounds of the loops, defined once: status marks a loop stalled with
// them and the loop-stalled alert rule uses literals equal to them.
//
// A loop is stalled when a pass has run longer than InFlightStallBound (any
// loop), or when the time since its last pass ended exceeds its age bound. Only
// the loops with a decided age bound have one; every other loop is judged by
// the in-flight rule alone, and parser_drain (event-driven, so it may idle
// indefinitely) never by age.
const (
	// InFlightStallBound is the age of a running pass above which its loop is
	// stalled.
	InFlightStallBound = 600 * time.Second
	// TrackerStallBound is the age bound of tracker_tick and tracker_poll.
	TrackerStallBound = 120 * time.Second
	// StatusRefreshStallBound is the age bound of status_refresh.
	StatusRefreshStallBound = 60 * time.Second
	// WatcherSweepStallFactor multiplies sweep.interval (config SweepInterval,
	// 15 minutes by default) into the age bound of watcher_sweep.
	WatcherSweepStallFactor = 3
)

// StallAgeBound is the largest time since loop's last pass ended before it
// counts as stalled; ok is false for a loop with no age bound. sweepInterval is
// the configured sweep.interval.
func StallAgeBound(loop string, sweepInterval time.Duration) (bound time.Duration, ok bool) {
	switch loop {
	case LoopWatcherSweep:
		return WatcherSweepStallFactor * sweepInterval, true
	case LoopTrackerTick, LoopTrackerPoll:
		return TrackerStallBound, true
	case LoopStatusRefresh:
		return StatusRefreshStallBound, true
	}
	return 0, false
}

// LoopStalled reports whether loop is stalled. hasAge is false for a loop that
// has never completed a pass (it has no age); age is the time since its last
// pass ended and inFlight the age of its oldest running pass (0 when none).
func LoopStalled(loop string, hasAge bool, age, inFlight, sweepInterval time.Duration) bool {
	if inFlight > InFlightStallBound {
		return true
	}
	if !hasAge {
		return false
	}
	bound, ok := StallAgeBound(loop, sweepInterval)
	return ok && age > bound
}
