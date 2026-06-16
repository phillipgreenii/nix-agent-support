package session

import "os"

// SessionExister probes whether a Claude session is resumable on this machine
// (its transcript file still exists). It is a seam so the service can be tested
// with a fake and never touch the real filesystem.
//
// The path is the AUTHORITATIVE transcript path Claude reported via the hook and
// that ccpool persisted on the row (ADR 0015) — not a reconstructed guess.
// Resumability is a FACT (does the transcript still exist on disk?), not a stored
// state. It is best-effort: on-disk does not always mean resumable (a corrupt or
// mid-turn transcript may still fail to resume), but its absence is a reliable
// "gone".
type SessionExister interface {
	Exists(transcriptPath string) bool
}

// fsSessionExister is the production SessionExister: it stats the recorded
// transcript path directly.
type fsSessionExister struct{}

// Exists reports whether transcriptPath names an existing regular file (not a
// directory and not absent).
func (fsSessionExister) Exists(transcriptPath string) bool {
	if transcriptPath == "" {
		return false
	}
	fi, err := os.Stat(transcriptPath)
	return err == nil && !fi.IsDir()
}

// NewFSSessionExister builds the production SessionExister, which stats the
// hook-recorded transcript path on the local filesystem.
func NewFSSessionExister() SessionExister { return fsSessionExister{} }
