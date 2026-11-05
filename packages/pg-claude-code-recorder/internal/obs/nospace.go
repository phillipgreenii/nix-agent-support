package obs

import (
	"errors"
	"strings"
	"syscall"
)

// The recorder alerts when it cannot do its own job for lack of space (operator
// ruling, Phillip, 2026-11-05: "it can alert me that it can't do its own job
// because insufficient space ... a generic alert that there isn't less than 20gb
// space available is not in the scope of this tool."). The signal keys on the
// recorder's own refusal or failure of one named operation, never on a host
// free-space level.

// The operations that can be blocked for lack of space (label op of
// MBlockedNoSpace).
const (
	OpCopy            = "copy"             // copying a file into the store (ingest)
	OpParseIndex      = "parse_index"      // parse and index writes
	OpArchive         = "archive"          // compression and archive segment writes (settle)
	OpRemovalBarrier  = "removal_barrier"  // the durability barrier that gates a removal
	OpIndexCheckpoint = "index_checkpoint" // the WAL checkpoint and truncate
	OpRebuild         = "rebuild"          // the rebuild free-space preflight
)

// NoSpaceOps lists every op label value, so each series exists from start.
var NoSpaceOps = []string{OpCopy, OpParseIndex, OpArchive, OpRemovalBarrier, OpIndexCheckpoint, OpRebuild}

// IsNoSpace reports whether err is the operating system or SQLite saying the
// volume is full (ENOSPC, EDQUOT, "database or disk is full").
func IsNoSpace(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no space left on device") || strings.Contains(m, "database or disk is full") ||
		strings.Contains(m, "disk quota exceeded")
}

// NoteNoSpace counts and logs "the recorder could not do op because of
// insufficient space" when err is a no-space failure, and reports whether it
// was. needed and had are byte counts as strings, "" meaning unknown.
func (o *Obs) NoteNoSpace(op string, err error, needed, had string) bool {
	if !IsNoSpace(err) {
		return false
	}
	o.NoteBlocked(op, err.Error(), needed, had)
	return true
}

// NoteBlocked records a space refusal the recorder decided itself (a preflight):
// the same counter and event as NoteNoSpace.
func (o *Obs) NoteBlocked(op, why, needed, had string) {
	if needed == "" {
		needed = "unknown"
	}
	if had == "" {
		had = "unknown"
	}
	o.Reg.Add(MBlockedNoSpace, map[string]string{"op": op}, 1)
	o.Event(EvBlockedNoSpace, "op", op, "needed_bytes", needed, "had_bytes", had, "why", why)
}
