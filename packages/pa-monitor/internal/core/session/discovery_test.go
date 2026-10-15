package session

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func mustWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverReadsFilesAndKeepsDeadPids(t *testing.T) {
	dir := t.TempDir()
	// Write three session files. PIDs: 100 (alive), 200 (dead), 300 (alive).
	mustWrite(t, dir, "a.json", `{"pid":100,"sessionId":"a","cwd":"/p"}`)
	mustWrite(t, dir, "b.json", `{"pid":200,"sessionId":"b","cwd":"/p"}`)
	mustWrite(t, dir, "c.json", `{"pid":300,"sessionId":"c","cwd":"/p"}`)

	d := Discoverer{
		SessionsDir: dir,
		PidAlive: func(pid int) bool {
			return pid == 100 || pid == 300
		},
	}
	out, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d sessions, want 3 (no dead-PID filter)", len(out))
	}
	byID := map[string]*Session{}
	for _, s := range out {
		byID[s.SessionID] = s
	}
	if !byID["a"].PidAlive {
		t.Errorf("session a: PidAlive false, want true")
	}
	if byID["b"].PidAlive {
		t.Errorf("session b: PidAlive true, want false")
	}
	if !byID["c"].PidAlive {
		t.Errorf("session c: PidAlive false, want true")
	}
}

func TestDiscoverSkipsMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":1,"sessionId":"s1","cwd":"/x","kind":"interactive"}`); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir+"/2.json", `{not json`); err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover should not fail on malformed file, got %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1 session (malformed skipped), got %d", len(got))
	}
}

func TestDiscover_PopulatesEnvViaInjectedReader(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":42,"sessionId":"s1","cwd":"/x"}`); err != nil {
		t.Fatal(err)
	}
	envByPID := map[int]map[string]string{
		42: {"TMUX": "/tmp/tmux-501/default,1,0", "CMUX_WORKSPACE_ID": "ws1"},
	}
	var gotSID string
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv: func(sessionID string, pid int) (map[string]string, error) {
			gotSID = sessionID
			return envByPID[pid], nil
		},
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions", len(got))
	}
	if gotSID != got[0].SessionID {
		t.Errorf("ReadEnv sessionID = %q, want %q (session-id must be threaded)", gotSID, got[0].SessionID)
	}
	if got[0].Env["TMUX"] != "/tmp/tmux-501/default,1,0" {
		t.Errorf("env TMUX = %q", got[0].Env["TMUX"])
	}
	if got[0].Env["CMUX_WORKSPACE_ID"] != "ws1" {
		t.Errorf("env CMUX_WORKSPACE_ID = %q", got[0].Env["CMUX_WORKSPACE_ID"])
	}
}

func TestDiscover_EmptyEnvOnReaderFailure(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":42,"sessionId":"s1"}`); err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv: func(_ string, _ int) (map[string]string, error) {
			return nil, os.ErrPermission
		},
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions", len(got))
	}
	if len(got[0].Env) != 0 {
		t.Errorf("expected empty env on reader failure, got %+v", got[0].Env)
	}
}

// TestDiscoverRejectsReusedPidWithMismatchingProcStart pins the PID-reuse guard
// with an injected live-start-time reader. It used to call the real `ps`, which
// is not on PATH in the nix build sandbox (pa-monitor-go-tests carries only git),
// so the guard failed open there and the reused row stayed alive (pg2-w0j0t).
func TestDiscoverRejectsReusedPidWithMismatchingProcStart(t *testing.T) {
	dir := t.TempDir()
	// The PID is "alive", but a row whose procStart is years off the live
	// process's start time belongs to a dead session whose PID was recycled.
	const pid = 4242
	live := time.Date(2026, time.October, 14, 7, 51, 50, 0, time.UTC)
	mustWrite(t, dir, "reused.json", fmt.Sprintf(`{"pid":%d,"sessionId":"reused","cwd":"/p","procStart":"Mon Jan  1 00:00:00 2001"}`, pid))
	mustWrite(t, dir, "match.json", fmt.Sprintf(`{"pid":%d,"sessionId":"match","cwd":"/p","procStart":"Wed Oct 14 07:51:51 2026"}`, pid))
	mustWrite(t, dir, "legacy.json", fmt.Sprintf(`{"pid":%d,"sessionId":"legacy","cwd":"/p"}`, pid))

	d := Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv:     func(string, int) (map[string]string, error) { return nil, nil },
		ProcStart: func(p int) (time.Time, error) {
			if p != pid {
				t.Errorf("ProcStart called with pid %d, want %d", p, pid)
			}
			return live, nil
		},
	}
	out, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byID := map[string]*Session{}
	for _, s := range out {
		byID[s.SessionID] = s
	}
	if byID["reused"] == nil || byID["reused"].PidAlive {
		t.Errorf("reused: want present with PidAlive=false, got %+v", byID["reused"])
	}
	if byID["match"] == nil || !byID["match"].PidAlive {
		t.Errorf("match (procStart within tolerance of live start): want PidAlive=true, got %+v", byID["match"])
	}
	if byID["legacy"] == nil || !byID["legacy"].PidAlive {
		t.Errorf("legacy (no procStart): want PidAlive=true (PID-only fallback), got %+v", byID["legacy"])
	}
}

// A reader that cannot determine the live start time fails open: the row keeps
// the PidAlive hook's verdict rather than being declared dead.
func TestDiscoverProcStartReaderErrorFailsOpen(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "r.json", `{"pid":7,"sessionId":"r","cwd":"/p","procStart":"Mon Jan  1 00:00:00 2001"}`)
	d := Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv:     func(string, int) (map[string]string, error) { return nil, nil },
		ProcStart:   func(int) (time.Time, error) { return time.Time{}, os.ErrNotExist },
	}
	out, err := d.Discover()
	if err != nil || len(out) != 1 {
		t.Fatalf("Discover: out=%v err=%v", out, err)
	}
	if !out[0].PidAlive {
		t.Errorf("unreadable live start time must fail open, got PidAlive=false")
	}
}
