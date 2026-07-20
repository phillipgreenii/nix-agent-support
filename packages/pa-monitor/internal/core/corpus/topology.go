package corpus

import (
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// sessionTopology is the Monitor's per-session resolution + activity summary,
// read by the poller in place of the inline ResolveTranscript + maxActivity.
type sessionTopology struct {
	resolvedPath string
	mtime        time.Time
	maxActivity  time.Time
	ok           bool
}

// projDir returns claudeHome/projects/<slug> for a session's cwd, reusing
// session.Session.TranscriptPath so the slug rule stays single-sourced.
func projDir(claudeHome string, s *session.Session) string {
	return filepath.Dir(s.TranscriptPath(claudeHome))
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
