package session

import (
	"os"
	"path/filepath"
	"strings"
)

// claudeSessionExists reports whether a resumable transcript exists for csid
// under the recorded cwd: <home>/.claude/projects/<encoded-cwd>/<csid>.jsonl
// (ADR 0015). Resumability is a FACT — does the Claude session still exist on
// the machine — not a stored state. It is best-effort: on-disk does not always
// mean resumable (a corrupt/mid-turn transcript may still fail to resume), but
// its absence is a reliable "gone".
//
// The encoding mirrors Claude's: <encoded-cwd> replaces each run of the OS path
// separator with a single '-' (so a leading separator yields a leading '-').
func claudeSessionExists(home, cwd, claudeSessionID string) bool {
	if home == "" || claudeSessionID == "" {
		return false
	}
	path := filepath.Join(home, ".claude", "projects", encodeProjectDir(cwd), claudeSessionID+".jsonl")
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// encodeProjectDir encodes a cwd into Claude's project-dir name: every run of
// the OS path separator collapses to a single '-'.
func encodeProjectDir(cwd string) string {
	sep := string(os.PathSeparator)
	// Collapse runs of the separator to a single one, then swap for '-' so a
	// path like "/a//b" maps the same as "/a/b".
	for strings.Contains(cwd, sep+sep) {
		cwd = strings.ReplaceAll(cwd, sep+sep, sep)
	}
	return strings.ReplaceAll(cwd, sep, "-")
}

// SessionExister probes whether a Claude session is resumable on this machine
// (the transcript still exists under the recorded cwd). It is a seam so the
// service can be tested with a fake and never touch a real ~/.claude.
type SessionExister interface {
	Exists(cwd, claudeSessionID string) bool
}

// homeSessionExister is the production SessionExister: it probes the real
// ~/.claude under the given home dir.
type homeSessionExister struct{ home string }

func (h homeSessionExister) Exists(cwd, claudeSessionID string) bool {
	return claudeSessionExists(h.home, cwd, claudeSessionID)
}

// NewHomeSessionExister builds the production SessionExister rooted at home
// (typically os.UserHomeDir()).
func NewHomeSessionExister(home string) SessionExister { return homeSessionExister{home: home} }
