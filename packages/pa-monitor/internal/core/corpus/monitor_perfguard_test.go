package corpus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestScan_SteadyStateNoRework is the phase-1a perf guard. On an UNCHANGED
// corpus, a second Scan must do no re-work: no title re-probe (write-once cache),
// no transcript re-parse (cache_hit), no subagent file re-read (unchanged
// (size,mtime)), and each active session's subagents dir ReadDir'd EXACTLY ONCE
// — the pg2-fvuk1 requirement (today's poller ReadDirs it twice: maxActivity +
// LastSubagentError).
func TestScan_SteadyStateNoRework(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const nSessions = 2
	for i := 0; i < nSessions; i++ {
		cwd := filepath.Join("/tmp", "proj", string(rune('a'+i)))
		dir := projectDir(t, home, cwd)
		sid := "s" + string(rune('a'+i))
		writeSessionRecord(t, sessionsDir, 100+i, sid, cwd, "Name"+sid)
		// custom-title beyond the old 200-line cap, to exercise the real probe once.
		main := writeTranscript(t, dir, sid+".jsonl", time.Unix(1000, 0),
			append(fillerLines(400), titleLine("Name"+sid), assistantLine("m", 5, 3))...)
		sub := subagentsDirFor(t, main)
		writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1500, 0),
			apiErrorLine("server_error", "boom", "2026-07-20T10:00:00Z"))
	}

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorWithObservers(home, disc)

	// Cold scan: real work happens.
	if _, err := m.Scan(time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	if m.TitleProbesLastScan() == 0 || m.TranscriptScansLastScan() == 0 || m.SubagentFileReadsLastScan() == 0 {
		t.Fatalf("cold scan did no work: probes=%d scans=%d reads=%d",
			m.TitleProbesLastScan(), m.TranscriptScansLastScan(), m.SubagentFileReadsLastScan())
	}

	// Steady-state scan on the unchanged corpus.
	if _, err := m.Scan(time.Unix(3000, 0)); err != nil {
		t.Fatal(err)
	}
	if got := m.TitleProbesLastScan(); got != 0 {
		t.Errorf("TitleProbesLastScan = %d, want 0 (write-once title cache)", got)
	}
	if got := m.TranscriptScansLastScan(); got != 0 {
		t.Errorf("TranscriptScansLastScan = %d, want 0 (all cache_hit)", got)
	}
	if got := m.SubagentFileReadsLastScan(); got != 0 {
		t.Errorf("SubagentFileReadsLastScan = %d, want 0 (unchanged agent files reused)", got)
	}
	if got := m.SubagentReadDirsLastScan(); got != nSessions {
		t.Errorf("SubagentReadDirsLastScan = %d, want %d (each session's subagents dir ReadDir'd ONCE, not twice — pg2-fvuk1)", got, nSessions)
	}
}
