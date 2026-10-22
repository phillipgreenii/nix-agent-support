package bgkill

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// startProc starts a real long-lived process whose command line contains argv0
// tag (bash keeps its argv because `:` is the last command, so no exec
// replaces it), and reaps it on cleanup.
func startProc(t *testing.T, tag string) int {
	t.Helper()
	cmd := exec.Command("bash", "-c", "sleep 60; :", tag)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// writeJob lays down a record in bgrun's real on-disk format.
func writeJob(t *testing.T, dir, name string, pid int, meta string, exit string) {
	t.Helper()
	write := func(ext, content string) {
		if err := os.WriteFile(filepath.Join(dir, name+"."+ext), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pid", strconv.Itoa(pid)+"\n")
	write("meta", meta)
	if exit != "" {
		write("exit", exit+"\n")
	}
}

func startOf(t *testing.T, pid int) string {
	t.Helper()
	s, err := psField(pid, "lstart=")
	if err != nil || s == "" {
		t.Fatalf("ps lstart for %d: %q %v", pid, s, err)
	}
	return s
}

func TestFileStore_Job(t *testing.T) {
	// The darwin nix sandbox denies ps; the identity probe cannot run there. It
	// runs on a normal host, in the commit hook and on a Linux builder with procps.
	if s, err := psField(os.Getpid(), "lstart="); err != nil || s == "" {
		t.Skipf("ps cannot inspect processes here: %q %v", s, err)
	}
	dir := t.TempDir()
	store := &FileStore{Dir: dir}
	live := startProc(t, "bgrun-fixture")
	liveStart := startOf(t, live)
	notBgrun := startProc(t, "something-else")
	// A real pid that is certainly dead: started and reaped.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}

	meta := func(session, pidstart string) string {
		m := ""
		if session != "" {
			m += "session: " + session + "\n"
		}
		m += "cmd: sleep 60\ncwd: /tmp\nstart: 2026-10-22T00:00:00Z\n"
		if pidstart != "" {
			m += "pidstart: " + pidstart + "\n"
		}
		return m
	}

	writeJob(t, dir, "live", live, meta("sess-A", liveStart), "")
	writeJob(t, dir, "live-no-pidstart", live, meta("sess-A", ""), "")
	writeJob(t, dir, "wrong-start", live, meta("sess-A", "Mon Jan  1 00:00:00 2001"), "")
	writeJob(t, dir, "not-bgrun", notBgrun, meta("sess-A", startOf(t, notBgrun)), "")
	writeJob(t, dir, "dead", dead.Process.Pid, meta("sess-A", ""), "")
	writeJob(t, dir, "done", live, meta("sess-A", liveStart), "0")
	writeJob(t, dir, "nosession", live, meta("", liveStart), "")
	writeJob(t, dir, "pid1", 1, meta("sess-A", ""), "")
	// A later free-text line (cwd with an embedded newline) must not shadow session.
	writeJob(t, dir, "shadow", live, "session: sess-A\ncmd: x\ncwd: /a\nsession: forged\n", "")

	tests := []struct {
		name      string
		wantFound bool
		want      Job
	}{
		{"live", true, Job{Session: "sess-A", LauncherAlive: true}},
		{"live-no-pidstart", true, Job{Session: "sess-A", LauncherAlive: true}},
		{"wrong-start", true, Job{Session: "sess-A"}},
		{"not-bgrun", true, Job{Session: "sess-A"}},
		{"dead", true, Job{Session: "sess-A"}},
		{"done", true, Job{Session: "sess-A", Finished: true, LauncherAlive: true}},
		{"nosession", true, Job{LauncherAlive: true}},
		{"pid1", true, Job{Session: "sess-A"}},
		{"shadow", true, Job{Session: "sess-A", LauncherAlive: true}},
		{"ghost", false, Job{}},
		{"../escape", false, Job{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := store.Job(tt.name)
			if found != tt.wantFound || got != tt.want {
				t.Errorf("Job(%q) = %+v, %v; want %+v, %v", tt.name, got, found, tt.want, tt.wantFound)
			}
		})
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("BG_DIR", "/explicit/dir")
	if got := DefaultDir(); got != "/explicit/dir" {
		t.Errorf("BG_DIR: got %q", got)
	}
	t.Setenv("BG_DIR", "")
	t.Setenv("TMPDIR", "/var/folders/xx/T/")
	t.Setenv("USER", "alice")
	if got := DefaultDir(); got != "/var/folders/xx/T/pg-bg-alice" {
		t.Errorf("TMPDIR/USER: got %q", got)
	}
	t.Setenv("TMPDIR", "")
	if got := DefaultDir(); !strings.HasPrefix(got, "/tmp/pg-bg-") {
		t.Errorf("empty TMPDIR: got %q", got)
	}
}
