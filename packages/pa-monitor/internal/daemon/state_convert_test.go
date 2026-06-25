package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// convertSessionWithContribution must carry LastError.FromSubagent through from
// the persisted column so the gRPC GetState/WatchState path (and the TUI it
// feeds) shows the '(in subagent)' provenance after a daemon restart. See
// pg2-kg8u.
func TestConvertSessionWithContribution_PreservesLastErrorFromSubagent(t *testing.T) {
	sc := &store.SessionWithContribution{
		Session: store.Session{
			SessionID:             "sid-1",
			LastErrorKind:         "server_error",
			LastErrorText:         "API Error: Stream idle timeout",
			LastErrorAt:           time.Now().UTC(),
			LastErrorTerminal:     true,
			LastErrorFromSubagent: true,
		},
	}

	sv := convertSessionWithContribution(sc)
	if sv == nil {
		t.Fatal("convertSessionWithContribution returned nil")
	}
	if sv.SessionEnrichment.LastError == nil {
		t.Fatal("LastError is nil; expected reconstructed error record")
	}
	if !sv.SessionEnrichment.LastError.FromSubagent {
		t.Error("LastError.FromSubagent = false; want true (provenance dropped on the DB->client path)")
	}
}
