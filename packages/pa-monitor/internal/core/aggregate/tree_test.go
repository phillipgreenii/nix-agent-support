package aggregate

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func authSession(sid string, kind transcript.ErrorKind, terminal bool) *SessionView {
	return &SessionView{
		Session: &session.Session{SessionID: sid},
		SessionEnrichment: SessionEnrichment{
			LastError: &transcript.ErrorRecord{Kind: kind, IsTerminal: terminal},
		},
	}
}

func TestAuthFailedCount(t *testing.T) {
	tree := &Tree{Dirs: []*Directory{{Sessions: []*SessionView{
		authSession("a", transcript.ErrAuthFailed, true),  // counts
		authSession("b", transcript.ErrAuthFailed, false), // not terminal — skip
		authSession("c", transcript.ErrServerError, true), // wrong kind — skip
		{Session: &session.Session{SessionID: "d"}},       // no error — skip
		authSession("e", transcript.ErrAuthFailed, true),  // counts
	}}}}

	if got := tree.AuthFailedCount(); got != 2 {
		t.Errorf("AuthFailedCount() = %d, want 2", got)
	}
	if got := (*Tree)(nil).AuthFailedCount(); got != 0 {
		t.Errorf("nil tree AuthFailedCount() = %d, want 0", got)
	}
}
