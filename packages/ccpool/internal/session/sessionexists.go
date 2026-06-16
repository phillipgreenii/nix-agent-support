package session

import (
	"os"
	"path/filepath"
	"strings"
)

// isAlphaNum reports whether r is in [A-Za-z0-9] — the only characters Claude
// preserves when encoding a cwd into a project-dir name.
func isAlphaNum(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	default:
		return false
	}
}

// claudeSessionExists reports whether a resumable transcript exists for csid
// under the recorded cwd: <home>/.claude/projects/<encoded-cwd>/<csid>.jsonl
// (ADR 0015). Resumability is a FACT — does the Claude session still exist on
// the machine — not a stored state. It is best-effort: on-disk does not always
// mean resumable (a corrupt/mid-turn transcript may still fail to resume), but
// its absence is a reliable "gone".
//
// The encoding mirrors Claude's: <encoded-cwd> replaces every non-alphanumeric
// character with '-' (so a leading separator yields a leading '-').
func claudeSessionExists(home, cwd, claudeSessionID string) bool {
	if home == "" || claudeSessionID == "" {
		return false
	}
	path := filepath.Join(home, ".claude", "projects", encodeProjectDir(cwd), claudeSessionID+".jsonl")
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// encodeProjectDir encodes a cwd into Claude's project-dir name: every
// character that is not [A-Za-z0-9] is replaced with '-'. Runs are NOT
// collapsed and there is no separator-specific logic, so adjacent specials map
// to adjacent dashes — e.g. "/a/b_c/.d" → "-a-b-c--d" (the "/." becomes "--"),
// matching ~/.claude/projects/<encoded-cwd>/ exactly (verified against disk,
// ADR 0015).
func encodeProjectDir(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		if isAlphaNum(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
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
