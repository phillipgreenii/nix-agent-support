package recorderfacts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// startMs is a fixed start instant (no wall clock in the test).
const startMs = int64(1793375000000)

func writeRegistry(t *testing.T, e *clienttest.Env, pid int, sid, status, waiting string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,`+
		`"version":"2.1.0","kind":"interactive","entrypoint":"cli","status":%q,"waitingFor":%q,"statusUpdatedAt":%d,"updatedAt":%d}`,
		pid, sid, startMs, procStart(startMs), status, waiting, startMs+5000, startMs+5000)
	if err := os.WriteFile(filepath.Join(e.ClaudeDir, "sessions", fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	e.Settle()
}

// procStart formats the registry's procStart the way the recorder's process
// table does (the test reads it back from the recorder instead of guessing).
func procStart(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("Mon Jan _2 15:04:05 2006")
}

func TestRegistrationsReadTheRecordersProcessFacts(t *testing.T) {
	e := clienttest.Start(t)
	e.Procs.Add(501, time.UnixMilli(startMs), nil)
	e.Procs.Add(502, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 501, "example-live", "busy", "")
	writeRegistry(t, e, 502, "example-ended", "idle", "")
	e.Procs.Exit(502, time.UnixMilli(startMs+9000))

	f := New(client.Options{Socket: e.Socket})
	deadline := time.Now().Add(10 * time.Second)
	var got []*session.Session
	for {
		var err error
		got, err = f.Registrations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 2 && !byID(got, "example-ended").PidAlive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorder never reported the exit: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	live := byID(got, "example-live")
	if live == nil || !live.PidAlive || live.PID != 501 || live.Cwd != "/example/work" || live.RegistryStatus != "busy" {
		t.Errorf("live session = %+v", live)
	}
	if live.Kind != "interactive" || live.Entrypoint != "cli" {
		t.Errorf("registry bytes not carried: kind=%q entrypoint=%q", live.Kind, live.Entrypoint)
	}
	if want := time.UnixMilli(startMs + 5000); !live.StatusUpdatedAt.Equal(want) {
		t.Errorf("StatusUpdatedAt = %v, want %v", live.StatusUpdatedAt, want)
	}
	if got[0].SessionID != "example-live" {
		t.Errorf("alive Processes must come first: %v", got[0].SessionID)
	}
}

func byID(ss []*session.Session, id string) *session.Session {
	for _, s := range ss {
		if s.SessionID == id {
			return s
		}
	}
	return nil
}

// TestDaemonOfflineIsReportedWithoutFallback is the recorder-down path: nothing
// answers on the socket, so discovery says "daemon offline" and reads no file
// even though a registry file sits in the Claude directory.
func TestDaemonOfflineIsReportedWithoutFallback(t *testing.T) {
	e := clienttest.Start(t)
	e.Procs.Add(501, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 501, "example-live", "busy", "")
	f := New(client.Options{Socket: e.Socket})
	if got, err := f.Registrations(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("online: %v %v", got, err)
	}
	e.Stop()

	d := &session.Discoverer{Registry: f, ReadEnv: func(string, int) (map[string]string, error) { return nil, nil }}
	got, err := d.Discover()
	if !errors.Is(err, session.ErrDaemonOffline) {
		t.Fatalf("Discover while down: %v, want ErrDaemonOffline", err)
	}
	if len(got) != 0 {
		t.Errorf("fell back to Claude's files: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.ClaudeDir, "sessions", "501.json")); err != nil {
		t.Fatalf("fixture registry file should still exist (proves no fallback read): %v", err)
	}
}

func TestDiscovererAddsProcessEnvToRecorderRows(t *testing.T) {
	e := clienttest.Start(t)
	e.Procs.Add(501, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 501, "example-live", "idle", "")
	d := &session.Discoverer{
		Registry: New(client.Options{Socket: e.Socket}),
		ReadEnv: func(sid string, pid int) (map[string]string, error) {
			return map[string]string{"SID": sid, "PID": fmt.Sprint(pid)}, nil
		},
	}
	got, err := d.Discover()
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Env["SID"] != "example-live" || got[0].Env["PID"] != "501" {
		t.Errorf("env = %v", got[0].Env)
	}
}

// TestEndedSessionWhoseRegistryFileWasDeletedKeepsKindAndEntrypoint: when a
// session ends, Claude deletes its registry file and the recorder stores a
// newest record with no registry bytes. A startup lookup of that ended session
// must still read the last real registry version, or its stored row is blank.
func TestEndedSessionWhoseRegistryFileWasDeletedKeepsKindAndEntrypoint(t *testing.T) {
	e := clienttest.Start(t)
	e.Procs.Add(503, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 503, "example-deleted", "idle", "")
	e.Procs.Exit(503, time.UnixMilli(startMs+9000))
	if err := os.Remove(filepath.Join(e.ClaudeDir, "sessions", "503.json")); err != nil {
		t.Fatal(err)
	}
	e.Settle()

	f := New(client.Options{Socket: e.Socket})
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := f.Registrations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if s := byID(got, "example-deleted"); s != nil && !s.PidAlive {
			if s.Kind != "interactive" || s.Entrypoint != "cli" {
				t.Errorf("ended session read back blank: kind=%q entrypoint=%q", s.Kind, s.Entrypoint)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorder never reported the ended session: %+v", got)
		}
		f.ResyncEnded()
		time.Sleep(5 * time.Millisecond)
	}
}
