//go:build darwin

package metrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func listAll() ([]Proc, error) { return listProcsComm(func(string) bool { return true }) }

// startFake starts a real process (sh) whose argv holds
// fsmonitor--daemon, with working directory dir.
func startFake(t *testing.T, gitBin, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(gitBin, "-c", "read x", "fsmonitor--daemon")
	cmd.Dir = dir
	// `read` is a builtin blocking on the open stdin pipe: no fork, so the
	// table never shows a transient copy of the daemon's argv.
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestRealProcessTableCountsDaemonInSlots(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("no lsof")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A copy of a system binary is killed by code signing, and a symlink does
	// not rename the process (the kernel reports the real binary's name), so
	// the fake runs as sh and the test reads the table without the comm
	// prefilter. The production "git" prefilter is checked on the live host:
	// ListProcs finds the machine's real fsmonitor daemons.
	gitBin := filepath.Join(tmp, "git")
	if err := os.Symlink("/bin/sh", gitBin); err != nil {
		t.Fatal(err)
	}
	slots := filepath.Join(tmp, "slots")
	inside := filepath.Join(slots, "slot-01")
	outside := filepath.Join(tmp, "elsewhere")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in := startFake(t, gitBin, inside)

	// Wait for exec to settle: the table must show the daemon's pid and argv.
	var found *Proc
	for i := 0; i < 100 && found == nil; i++ {
		procs, err := listAll()
		if err != nil {
			t.Fatal(err)
		}
		for j := range procs {
			if procs[j].Pid == in.Process.Pid && containsStr(procs[j].Args, "fsmonitor--daemon") {
				found = &procs[j]
			}
		}
		if found == nil {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if found == nil {
		t.Fatal("real process table did not list the fake daemon")
	}
	if found.Start.IsZero() {
		t.Fatal("start time missing")
	}

	c := NewDaemonCounterWith(listAll, runLsof, time.Now)
	if n, err := c.CountDaemons(slots); err != nil || n != 1 {
		t.Fatalf("count = %d, %v; want 1", n, err)
	}

	// Negative control: an identical daemon with cwd outside the slots dir.
	startFake(t, gitBin, outside)
	time.Sleep(300 * time.Millisecond)
	if n, err := NewDaemonCounterWith(listAll, runLsof, time.Now).CountDaemons(slots); err != nil || n != 1 {
		t.Fatalf("count with an outside daemon = %d, %v; want still 1", n, err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseProcArgs2(t *testing.T) {
	b := []byte{2, 0, 0, 0}
	b = append(b, "/usr/bin/git\x00\x00\x00git\x00fsmonitor--daemon\x00ENV=1\x00"...)
	got := parseProcArgs2(b)
	if len(got) != 2 || got[0] != "git" || got[1] != "fsmonitor--daemon" {
		t.Fatalf("got %q", got)
	}
}
