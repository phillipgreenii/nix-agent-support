package corpus

import "github.com/phillipgreenii/pa-monitor/internal/core/transcript"

// SubagentErrorObserver holds the per-session latest-terminal subagent error
// projection (absent when none), folded by the Monitor's subagent tail. It
// replaces the poller's per-tick transcript.LastSubagentError scan.
type SubagentErrorObserver struct {
	errs map[string]*transcript.ErrorRecord
}

func NewSubagentErrorObserver() *SubagentErrorObserver {
	return &SubagentErrorObserver{errs: map[string]*transcript.ErrorRecord{}}
}

// Criteria: active sessions' subagent files. Position LastMatch is advisory; the
// fold keeps the latest-.At terminal record across files. No Window bound —
// dead-PID sessions are still folded until GC.
func (o *SubagentErrorObserver) Criteria() Criteria {
	return Criteria{Classes: []FileClass{Subagent}, ActiveOnly: true, Position: LastMatch}
}

// set records (or, with nil, clears) sessionID's terminal subagent error.
func (o *SubagentErrorObserver) set(sessionID string, rec *transcript.ErrorRecord) {
	if rec == nil {
		delete(o.errs, sessionID)
		return
	}
	o.errs[sessionID] = rec
}

// LastTerminal returns sessionID's latest terminal subagent error (nil, false
// when none).
func (o *SubagentErrorObserver) LastTerminal(sessionID string) (*transcript.ErrorRecord, bool) {
	r, ok := o.errs[sessionID]
	return r, ok
}

func (o *SubagentErrorObserver) Prune(activeIDs map[string]bool) {
	for id := range o.errs {
		if !activeIDs[id] {
			delete(o.errs, id)
		}
	}
}
