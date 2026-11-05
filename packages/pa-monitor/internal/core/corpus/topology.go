package corpus

import "time"

// sessionTopology is one session's resolved transcript (the recorder's file
// reference), the time of its last record, and the newest activity over the
// transcript and its subagent files.
type sessionTopology struct {
	resolvedPath string
	mtime        time.Time
	maxActivity  time.Time
	ok           bool
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
