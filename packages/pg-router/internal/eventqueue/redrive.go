package eventqueue

import (
	"strconv"
	"strings"
	"time"
)

// redriveSuffix separates an original event id from its redrive generation
// ("<id>~r<generation>"), so a redrive has a new id and de-duplication does not
// absorb it (ADR 0121 D3).
const redriveSuffix = "~r"

// RedriveBase returns the id of the ORIGINAL event a (possibly redriven) event id
// derives from: id with any redrive suffix removed.
func RedriveBase(id string) string {
	if i := strings.LastIndex(id, redriveSuffix); i >= 0 {
		if _, err := strconv.Atoi(id[i+len(redriveSuffix):]); err == nil {
			return id[:i]
		}
	}
	return id
}

// RedriveID returns the id of generation gen of the original event id.
func RedriveID(id string, gen int) string {
	return RedriveBase(id) + redriveSuffix + strconv.Itoa(gen)
}

// EntityOf returns the entity part of an event id: the text before the first "@"
// (ids that carry no "@" are their own entity). A source stamps "<entity>@<digest>"
// so every change of one entity shares the entity part (ADR 0121 D3).
func EntityOf(id string) string {
	entity, _, _ := strings.Cut(RedriveBase(id), "@")
	return entity
}

// AcceptedAfter reports whether listenerID has already accepted a LATER event for
// the same entity as eventID: an event of that entity, from another change (not a
// redrive of eventID's own chain), stamped after the instant at. A redrive is
// pointless then, since the handler works from the entity's current state (ADR
// 0121 D3).
func (q *Queue) AcceptedAfter(listenerID, eventID string, at time.Time) bool {
	entity, base := EntityOf(eventID), RedriveBase(eventID)
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, e := range q.entries {
		if RedriveBase(id) == base || EntityOf(id) != entity {
			continue
		}
		if e.evt.At.After(at) && e.accepted[listenerID] {
			return true
		}
	}
	return false
}
