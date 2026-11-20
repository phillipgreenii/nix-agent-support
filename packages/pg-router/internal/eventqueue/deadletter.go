package eventqueue

import (
	"sort"
	"time"
)

// Dead-letter failure reasons: why a dispatch ended terminally failed. The
// reason is chosen in this order: a deterministic class is always
// DeadReasonDeterministic; otherwise DeadReasonWindowClosed when the event had
// expired at the failure instant; otherwise DeadReasonRetriesSpent (which also
// covers a role that did not opt in to retries).
const (
	DeadReasonDeterministic = "deterministic"
	DeadReasonRetriesSpent  = "retries-spent"
	DeadReasonWindowClosed  = "window-closed"
)

// ClassDeterministic is the dead-letter class of any handler failure that is not
// one of the router's four transient classes.
const ClassDeterministic = "deterministic"

// DeadLetter is one failed record: a dispatch of EventID to Role that ended
// terminally failed with no re-run. Attempts is the 1-based attempt that ended it.
type DeadLetter struct {
	EventID  string
	Role     string
	Class    string
	Attempts int
	Reason   string
	At       time.Time
}

func (d DeadLetter) record() Record {
	return Record{
		Op: opFailed, EventID: d.EventID, ListenerID: d.Role, Class: d.Class,
		Attempts: d.Attempts, Reason: d.Reason, At: d.At,
	}
}

func deadLetterOf(r Record) DeadLetter {
	return DeadLetter{
		EventID: r.EventID, Role: r.ListenerID, Class: r.Class,
		Attempts: r.Attempts, Reason: r.Reason, At: r.At,
	}
}

func deadLettersOf(m map[string]Record) map[string]DeadLetter {
	out := make(map[string]DeadLetter, len(m))
	for k, r := range m {
		out[k] = deadLetterOf(r)
	}
	return out
}

func sortedDeadLetters(m map[string]DeadLetter) []DeadLetter {
	roles := make([]string, 0, len(m))
	for r := range m {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	out := make([]DeadLetter, 0, len(roles))
	for _, r := range roles {
		out = append(out, m[r])
	}
	return out
}

// RecordSettledFailure appends, to the log at path, the records of a one-shot
// dispatch that ran outside a daemon (pg-router run-role): the enqueue of evt, the
// role's accept of it and the failed record dl, so the dead-letters verb lists it.
// It takes the log lock like any writer, so it fails with *ErrLogLocked while a
// daemon owns the log.
func RecordSettledFailure(path string, evt Event, dl DeadLetter) error {
	s, err := NewFileStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC().Truncate(time.Second)
	evt = evt.Resolve(now)
	dl.At = dl.At.UTC().Truncate(time.Second)
	err = s.AppendBatch([]Record{
		recordFromEvent(evt, now),
		{Op: opAccept, EventID: evt.ID, ListenerID: dl.Role, At: dl.At, StartedAt: dl.At},
		dl.record(),
	})
	return err
}
