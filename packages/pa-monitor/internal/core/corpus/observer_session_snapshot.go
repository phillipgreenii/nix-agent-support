package corpus

import "github.com/phillipgreenii/pa-monitor/internal/core/transcript"

// SessionSnapshotObserver holds the per-session parsed transcript Snapshot
// projection (context tokens, model, first-prompt, awaiting-input,
// rate-limit-reset, LastError) folded by the Monitor's transcript tail. Its
// criteria gates it to active sessions' transcripts.
type SessionSnapshotObserver struct {
	snaps map[string]transcript.Snapshot
}

func NewSessionSnapshotObserver() *SessionSnapshotObserver {
	return &SessionSnapshotObserver{snaps: map[string]transcript.Snapshot{}}
}

// Criteria: active sessions' transcripts, all lines (the fold consumes the whole
// transcript). No Window bound — a dead-PID session's transcript is still tailed
// until GC (ActiveOnly includes dead-PID sessions per the Monitor's active set).
func (o *SessionSnapshotObserver) Criteria() Criteria {
	return Criteria{Classes: []FileClass{Transcript}, ActiveOnly: true, Position: AllLines}
}

func (o *SessionSnapshotObserver) set(sessionID string, snap transcript.Snapshot) {
	o.snaps[sessionID] = snap
}

// Snapshot returns sessionID's folded Snapshot and whether one is present.
func (o *SessionSnapshotObserver) Snapshot(sessionID string) (transcript.Snapshot, bool) {
	s, ok := o.snaps[sessionID]
	return s, ok
}

func (o *SessionSnapshotObserver) Prune(activeIDs map[string]bool) {
	for id := range o.snaps {
		if !activeIDs[id] {
			delete(o.snaps, id)
		}
	}
}
