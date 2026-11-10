package originguard

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
	"time"
)

// PresenceState is what the presence file says about the human.
type PresenceState string

// The presence states. PresenceUnknown is anything missing, stale or unreadable.
const (
	PresencePresent PresenceState = "present"
	PresenceIdle    PresenceState = "idle"
	PresenceAway    PresenceState = "away"
	PresenceAsleep  PresenceState = "asleep"
	PresenceUnknown PresenceState = "unknown"
)

// Presence is one reading. Why names the reason a reading is unknown (for
// diagnostics only).
type Presence struct {
	State PresenceState
	Why   string
}

// NoHuman reports whether the reading means no human is at the machine. Only
// asleep and away do. idle counts as a human, and so does everything unknown.
func (p Presence) NoHuman() bool {
	return p.State == PresenceAsleep || p.State == PresenceAway
}

// PresenceSource reads the presence.
type PresenceSource interface{ Read() Presence }

// presenceDoc is the userpresence.json document written by host-activity.
type presenceDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	State         string `json:"state"`
	WrittenAt     string `json:"writtenAt"`
}

const maxPresenceBytes = 4096

// clockSkew is how far in the future a writtenAt may be before the file is
// treated as unreadable.
const clockSkew = 5 * time.Second

// FilePresence reads the host-activity presence file under the contract's
// rules: a regular file (no symlink), owned by the current user, not writable
// by group or others; unknown schema, unparsable content or a writtenAt older
// than max(MaxAge, 2 x RefreshSeconds) all read as unknown.
type FilePresence struct {
	Path           string
	Clock          Clock
	MaxAge         time.Duration
	RefreshSeconds int
	// UID is the current user. Nil means os.Getuid.
	UID func() uint32
}

// NewFilePresence builds a reader from the config.
func NewFilePresence(c PresenceConfig, clock Clock) *FilePresence {
	return &FilePresence{Path: c.File, Clock: clock, MaxAge: c.MaxAge, RefreshSeconds: c.RefreshSeconds}
}

// Limit is the oldest writtenAt age still trusted.
func (f *FilePresence) Limit() time.Duration {
	return max(f.MaxAge, 2*time.Duration(f.RefreshSeconds)*time.Second)
}

// Read returns the presence, or an unknown reading with the reason.
func (f *FilePresence) Read() Presence {
	unknown := func(why string) Presence { return Presence{State: PresenceUnknown, Why: why} }

	fi, err := os.Lstat(f.Path)
	if err != nil {
		return unknown("missing")
	}
	if !fi.Mode().IsRegular() {
		return unknown("not a regular file")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return unknown("no owner information")
	}
	uid := uint32(os.Getuid())
	if f.UID != nil {
		uid = f.UID()
	}
	if st.Uid != uid {
		return unknown("foreign owner")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return unknown("group or world writable")
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return unknown("unreadable")
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, maxPresenceBytes+1))
	if err != nil || len(data) > maxPresenceBytes {
		return unknown("unreadable")
	}
	var doc presenceDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return unknown("unparsable")
	}
	if doc.SchemaVersion != 1 {
		return unknown("unknown schema version")
	}
	written, err := time.Parse(time.RFC3339, doc.WrittenAt)
	if err != nil {
		return unknown("unparsable writtenAt")
	}
	age := f.Clock.Now().Sub(written)
	if age > f.Limit() {
		return unknown("stale")
	}
	if age < -clockSkew {
		return unknown("written in the future")
	}
	switch s := PresenceState(doc.State); s {
	case PresencePresent, PresenceIdle, PresenceAway, PresenceAsleep:
		return Presence{State: s}
	}
	return unknown("unknown state")
}
