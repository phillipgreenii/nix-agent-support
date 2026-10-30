package watcher

import "time"

// State is the watcher's persisted checkpoint. The serve packet implements
// Checkpoint over the store; tests use an in-memory fake.
type State struct {
	// EventID is the last FSEvents event id the watcher processed (0 = none).
	EventID uint64
	// EventTime is when EventID was recorded; the replay gap is measured from it.
	EventTime time.Time
	// VolumeUUID is the FSEvents UUID of the volume holding the roots.
	VolumeUUID string
	// LastSweep is the time of the last successful sweep.
	LastSweep time.Time
}

// Checkpoint persists State across restarts.
type Checkpoint interface {
	Load() (State, error)
	Save(State) error
}

// Decision is the start-up choice between replaying FSEvents history and
// sweeping.
type Decision struct {
	// Replay is true when FSEvents history from SinceID should be replayed.
	Replay  bool
	SinceID uint64
	// Gap is true when a checkpoint existed but replay is not safe, so the
	// downtime must be covered by a sweep (logged as fsevents_gap).
	Gap    bool
	Reason string
}

// Decide chooses replay versus sweep after downtime. FSEvents history is only
// trusted when the gap since the last recorded event is under maxGap and the
// volume UUID is unchanged: an old event id returns partial data silently.
func Decide(st State, curUUID string, now time.Time, maxGap time.Duration, canReplay bool) Decision {
	switch {
	case !canReplay:
		return Decision{Reason: "event source has no history"}
	case st.EventID == 0:
		return Decision{Reason: "no checkpoint"}
	case curUUID == "" || st.VolumeUUID != curUUID:
		return Decision{Gap: true, Reason: "volume uuid changed or unknown"}
	}
	gap := now.Sub(st.EventTime)
	if gap < 0 || gap >= maxGap {
		return Decision{Gap: true, Reason: "downtime gap too long for replay"}
	}
	return Decision{Replay: true, SinceID: st.EventID, Reason: "replay"}
}
