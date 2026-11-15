package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func sv(pid int, alive, inbox, known bool) *aggregate.SessionView {
	return &aggregate.SessionView{Session: &session.Session{
		SessionID: "s" + strconv.Itoa(pid), PID: pid, PidAlive: alive, ControlInbox: inbox, ControlInboxKnown: known,
	}}
}

func noEnv(string) string { return "" }

func TestControlDeliverable_RecorderFact(t *testing.T) {
	cases := []struct {
		name string
		sv   *aggregate.SessionView
		want bool
	}{
		{"alive with inbox passes", sv(10, true, true, true), true},
		{"alive without inbox fails (negative control)", sv(10, true, false, true), false},
		{"dead pid with inbox fails", sv(10, false, true, true), false},
		{"nil fails", nil, false},
	}
	for _, c := range cases {
		if got := controlDeliverable(c.sv, noEnv); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func mkInbox(t *testing.T, dir string, pid int, fifo bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "inbox", strconv.Itoa(pid)+".fifo")
	if fifo {
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// With the recorder unavailable (fact unknown), the real FIFO decides; a regular
// file at the same path must not count.
func TestControlDeliverable_DirectFIFOCheckWhenRecorderUnavailable(t *testing.T) {
	dir := t.TempDir()
	mkInbox(t, dir, 11, true)
	mkInbox(t, dir, 12, false)
	env := func(k string) string {
		if k == "PG_CLAUDE_CONTROL_STATE_DIR" {
			return dir
		}
		return ""
	}
	if !controlDeliverable(sv(11, true, false, false), env) {
		t.Error("alive pid with a real FIFO must be deliverable")
	}
	if controlDeliverable(sv(12, true, false, false), env) {
		t.Error("a regular file at the inbox path must NOT count as a FIFO")
	}
	if controlDeliverable(sv(13, true, false, false), env) {
		t.Error("no inbox file: not deliverable")
	}
	if controlDeliverable(sv(11, false, false, false), env) {
		t.Error("dead pid with a FIFO must not be deliverable")
	}
}

func TestControlStateDir_MirrorsListenSh(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := controlStateDir(get(map[string]string{"PG_CLAUDE_CONTROL_STATE_DIR": "/a", "XDG_STATE_HOME": "/x", "HOME": "/h"})); got != "/a" {
		t.Errorf("explicit dir: %q", got)
	}
	if got := controlStateDir(get(map[string]string{"XDG_STATE_HOME": "/x", "HOME": "/h"})); got != "/x/pg-claude-control" {
		t.Errorf("xdg: %q", got)
	}
	if got := controlStateDir(get(map[string]string{"HOME": "/h"})); got != "/h/.local/state/pg-claude-control" {
		t.Errorf("home: %q", got)
	}
	if got := controlStateDir(noEnv); got != "" {
		t.Errorf("none: %q", got)
	}
}

// The producer gate must launch no ps or tmux process. PATH holds recording
// shims for both; a control run proves the shim records when it is invoked.
func TestControlResolver_LaunchesNoPsOrTmux(t *testing.T) {
	bin, logPath := t.TempDir(), filepath.Join(t.TempDir(), "calls.log")
	for _, name := range []string{"ps", "tmux"} {
		script := "#!/bin/sh\necho " + name + " >> " + logPath + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	calls := func() int {
		b, _ := os.ReadFile(logPath)
		return len(b)
	}

	// Control: the shims really record.
	if err := exec.CommandContext(t.Context(), "ps").Run(); err != nil || calls() == 0 {
		t.Fatalf("shim control failed: err=%v calls=%d", err, calls())
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}

	state := t.TempDir()
	mkInbox(t, state, 3001, true)
	env := func(k string) string {
		if k == "PG_CLAUDE_CONTROL_STATE_DIR" {
			return state
		}
		return ""
	}
	dir := &aggregate.Directory{Path: "/p"}
	for i := range 22 {
		dir.Sessions = append(dir.Sessions, sv(2000+i, true, i%2 == 0, true))
	}
	dir.Sessions = append(dir.Sessions, sv(3001, true, false, false)) // direct FIFO path
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{dir}}
	res := newControlResolver(tree, env)
	for i := range 22 {
		if got, want := res.HasSurface(2000+i), i%2 == 0; got != want {
			t.Errorf("pid %d: got %v want %v", 2000+i, got, want)
		}
	}
	if !res.HasSurface(3001) {
		t.Error("FIFO-backed pid must be deliverable")
	}
	if res.HasSurface(99999) {
		t.Error("unknown pid must not be deliverable")
	}
	if n := calls(); n != 0 {
		t.Errorf("gate launched ps/tmux %d bytes of calls, want none", n)
	}
}
