package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/recorderfacts"
	"github.com/phillipgreenii/pa-monitor/internal/render"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

const recStartMs = int64(1793375000000)

func recorderWithLiveSession(t *testing.T) *clienttest.Env {
	t.Helper()
	e := clienttest.Start(t)
	e.Procs.Add(501, time.UnixMilli(recStartMs), nil)
	body := fmt.Sprintf(`{"pid":501,"sessionId":"example-live","cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":"busy","updatedAt":%d}`,
		recStartMs, time.UnixMilli(recStartMs).UTC().Format("Mon Jan _2 15:04:05 2006"), recStartMs)
	if err := os.WriteFile(filepath.Join(e.ClaudeDir, "sessions", "501.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	e.Settle()
	return e
}

// TestProducerReadsSessionsAndLivenessFromTheRecorder drives the producer with
// the real recorder client against an in-process recorder (no started daemon
// process): the session and its liveness come from the recorder's Process facts.
func TestProducerReadsSessionsAndLivenessFromTheRecorder(t *testing.T) {
	e := recorderWithLiveSession(t)
	facts := recorderfacts.New(client.Options{Socket: e.Socket})
	p := &Poller{
		Registry: facts,
		Facts:    facts,
		Now:      func() time.Time { return time.UnixMilli(recStartMs + 60000) },
	}
	ds, err := p.Producer().Assemble(context.Background(), time.UnixMilli(recStartMs+60000))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Offline || len(ds.Sessions) != 1 {
		t.Fatalf("offline=%v sessions=%d", ds.Offline, len(ds.Sessions))
	}
	s := ds.Sessions[0]
	if s.SessionID != "example-live" || s.PID != 501 || !s.PidAlive || s.Cwd != "/example/work" {
		t.Errorf("session = %+v", s)
	}
}

// TestProducerReportsDaemonOfflineAndDoesNotFallBack is the recorder-down path:
// the state says "daemon offline", no session is invented from the registry
// file that still sits in the Claude directory, and the status row says so.
func TestProducerReportsDaemonOfflineAndDoesNotFallBack(t *testing.T) {
	e := recorderWithLiveSession(t)
	e.Stop()
	facts := recorderfacts.New(client.Options{Socket: e.Socket})
	p := &Poller{
		Registry: facts,
		Facts:    facts,
		Now:      func() time.Time { return time.UnixMilli(recStartMs + 60000) },
	}
	ds, err := p.Producer().Assemble(context.Background(), time.UnixMilli(recStartMs+60000))
	if err != nil {
		t.Fatal(err)
	}
	if !ds.Offline || len(ds.Sessions) != 0 {
		t.Fatalf("offline=%v sessions=%d, want offline with no sessions", ds.Offline, len(ds.Sessions))
	}
	if !render.IsDaemonOffline(ds.CostProbeErr) {
		t.Errorf("CostProbeErr = %v, want daemon offline", ds.CostProbeErr)
	}
	if _, serr := os.Stat(filepath.Join(e.ClaudeDir, "sessions", "501.json")); serr != nil {
		t.Fatalf("fixture registry file must still exist: %v", serr)
	}
}
