package corpus

import "time"

// FileClass identifies the kind of corpus file the Monitor tails.
type FileClass int

const (
	// Transcript is a conversation transcript: <slug>/<id>.jsonl (excludes the
	// <id>.status.jsonl rate_limits sibling — see session.IsTranscriptFile).
	Transcript FileClass = iota
	// Subagent is a per-subagent transcript: <slug>/<id>/subagents/agent-*.jsonl.
	Subagent
)

// Position is an ADVISORY reduction hint, not a gate: the Monitor reads a file
// once for all subscribers, so Position only informs an observer's own fold. 1a
// observers use AllLines.
type Position int

const (
	AllLines Position = iota
	FirstMatch
	LastMatch
)

// Criteria GATES which files the Monitor opens for an observer. An observer that
// declares no Classes matches nothing.
type Criteria struct {
	// Classes restricts the observer to these file classes.
	Classes []FileClass
	// Window, when > 0, restricts the observer to files whose mtime is within
	// now-Window; 0 disables the age gate (open regardless of age).
	Window time.Duration
	// ActiveOnly, when true, restricts the observer to files owned by a
	// discovered session (alive OR dead-PID pre-GC — see the Monitor's active
	// set), not merely a live PID.
	ActiveOnly bool
	// Position is an advisory reduction hint (see the type doc).
	Position Position
}

// matches reports whether a file of the given class, mtime, and active-ownership
// satisfies this criteria's gates as of now.
func (c Criteria) matches(class FileClass, mtime time.Time, isActive bool, now time.Time) bool {
	if !classIn(c.Classes, class) {
		return false
	}
	if c.Window > 0 && mtime.Before(now.Add(-c.Window)) {
		return false
	}
	if c.ActiveOnly && !isActive {
		return false
	}
	return true
}

func classIn(classes []FileClass, class FileClass) bool {
	for _, x := range classes {
		if x == class {
			return true
		}
	}
	return false
}

// Observer declares which files it cares about (for gating) and prunes state for
// sessions absent from the active set. 1a keeps observers concrete-typed (the
// Monitor drives a class-specific fold and populates the typed store); the
// generic per-line Event firehose is deferred to phase 1b, where UsagePricing
// becomes a second consumer of the transcript decode. The interface exists here
// only for criteria gating and pruning.
type Observer interface {
	Criteria() Criteria
	// Prune drops projection state for sessions absent from activeIDs this Scan.
	Prune(activeIDs map[string]bool)
}
