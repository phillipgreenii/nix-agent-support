package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// stubEnv keeps Discoverer hermetic (no ps -E subprocess per session).
func stubEnv(int) (map[string]string, error) { return map[string]string{}, nil }

func writeSessionRecord(t *testing.T, sessionsDir string, pid int, sessID, cwd, name string) {
	t.Helper()
	nameField := ""
	if name != "" {
		nameField = fmt.Sprintf(`,"name":%q`, name)
	}
	js := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"%s}`,
		pid, sessID, cwd, nameField)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newMonitorWithObservers(home string, disc *session.Discoverer) (*Monitor, *SessionSnapshotObserver, *SubagentErrorObserver) {
	m := New(home, disc)
	so := NewSessionSnapshotObserver()
	seo := NewSubagentErrorObserver()
	m.Register(so)
	m.Register(seo)
	return m, so, seo
}

func TestScan_PopulatesProjectionsAndTopology(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	cwd := "/tmp/proj"
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, cwd)

	writeSessionRecord(t, sessionsDir, 111, "s1", cwd, "Alpha")
	main := writeTranscript(t, dir, "s1.jsonl", time.Unix(1000, 0),
		titleLine("Alpha"), assistantLine("claude-x", 100, 50))
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1500, 0),
		apiErrorLine("server_error", "boom", "2026-07-20T10:00:00Z"))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorWithObservers(home, disc)

	sessions, err := m.Scan(time.Unix(2000, 0))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}

	snap, ok := m.SessionSnapshot("s1")
	if !ok || snap.TotalTokens != 50 || snap.Model != "claude-x" {
		t.Fatalf("SessionSnapshot(s1) = (%+v, %v), want tokens=50 model=claude-x", snap, ok)
	}
	subErr, ok := m.SubagentError("s1")
	if !ok || subErr == nil || subErr.Kind != transcript.ErrServerError || !subErr.FromSubagent {
		t.Fatalf("SubagentError(s1) = (%+v, %v), want server_error FromSubagent", subErr, ok)
	}
	gotPath, gotMtime, gotOK := m.ResolvedPath("s1")
	if !gotOK || gotPath != main || !gotMtime.Equal(time.Unix(1000, 0)) {
		t.Fatalf("ResolvedPath(s1) = (%q, %v, %v), want (%q, 1000, true)", gotPath, gotMtime, gotOK, main)
	}
	if got := m.MaxActivity("s1"); !got.Equal(time.Unix(1500, 0)) {
		t.Fatalf("MaxActivity(s1) = %v, want 1500 (subagent newer than transcript)", got)
	}
	// The session slice carries the resolved mtime for the poller.
	if !sessions[0].TranscriptMTime.Equal(time.Unix(1000, 0)) {
		t.Fatalf("session TranscriptMTime = %v, want 1000", sessions[0].TranscriptMTime)
	}
}

func TestScan_DeadPidSessionEnriched(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	cwd := "/tmp/dead"
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, cwd)
	writeSessionRecord(t, sessionsDir, 222, "dead1", cwd, "")
	writeTranscript(t, dir, "dead1.jsonl", time.Unix(1000, 0), assistantLine("claude-y", 10, 7))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return false }, ReadEnv: stubEnv}
	m, _, _ := newMonitorWithObservers(home, disc)
	sessions, err := m.Scan(time.Unix(2000, 0))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("Scan = (%d sessions, %v), want 1 dead-PID session enriched", len(sessions), err)
	}
	if sessions[0].PidAlive {
		t.Fatalf("expected dead PID")
	}
	snap, ok := m.SessionSnapshot("dead1")
	if !ok || snap.TotalTokens != 7 {
		t.Fatalf("dead-PID SessionSnapshot = (%+v, %v), want tokens=7 (still enriched)", snap, ok)
	}
}

func TestScan_PrunesVanishedSessionState(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dirA := projectDir(t, home, "/tmp/a")
	dirB := projectDir(t, home, "/tmp/b")
	writeSessionRecord(t, sessionsDir, 111, "sa", "/tmp/a", "")
	writeSessionRecord(t, sessionsDir, 222, "sb", "/tmp/b", "")
	mainA := writeTranscript(t, dirA, "sa.jsonl", time.Unix(1000, 0), assistantLine("m", 1, 1))
	mainB := writeTranscript(t, dirB, "sb.jsonl", time.Unix(1000, 0), assistantLine("m", 1, 1))
	// Subagents so the subagent-tail cache is populated (and thus prunable).
	writeTranscript(t, subagentsDirFor(t, mainA), "agent-1.jsonl", time.Unix(1000, 0), apiErrorLine("server_error", "x", "2026-07-20T10:00:00Z"))
	writeTranscript(t, subagentsDirFor(t, mainB), "agent-1.jsonl", time.Unix(1000, 0), apiErrorLine("server_error", "x", "2026-07-20T10:00:00Z"))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorWithObservers(home, disc)
	if _, err := m.Scan(time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(m.topo) != 2 || len(m.tt.cache) != 2 {
		t.Fatalf("after first scan: topo=%d tt.cache=%d, want 2/2", len(m.topo), len(m.tt.cache))
	}
	// Session sb vanishes.
	if err := os.Remove(filepath.Join(sessionsDir, "222.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(time.Unix(3000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(m.topo) != 1 || len(m.tt.cache) != 1 || len(m.st.cache) != 1 {
		t.Fatalf("after prune: topo=%d tt.cache=%d st.cache=%d, want 1/1/1", len(m.topo), len(m.tt.cache), len(m.st.cache))
	}
	if _, ok := m.SessionSnapshot("sb"); ok {
		t.Fatalf("vanished session sb still in SessionSnapshot projection")
	}
}
