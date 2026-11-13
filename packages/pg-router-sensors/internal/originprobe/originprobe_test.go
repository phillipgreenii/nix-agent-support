package originprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestProbeClasses(t *testing.T) {
	repo := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	cases := []struct {
		name string
		root string
		res  cmdrun.Result
		want Class
	}{
		{"ok", repo, cmdruntest.Out("abc\tHEAD"), OK},
		{"mount-missing: root absent", missing, cmdrun.Result{}, MountMissing},
		{"mount-missing: mkdir text", repo, cmdruntest.Fail(1, "mkdir /Volumes/x: permission denied"), MountMissing},
		{"auth: oauth", repo, cmdruntest.Fail(128, "fatal: oauth command timed out"), AuthUnavailable},
		{"auth: generic", repo, cmdruntest.Fail(128, "fatal: Could not read from remote repository."), AuthUnavailable},
		{"network", repo, cmdruntest.Fail(128, "ssh: Could not resolve hostname h"), Network},
		{"unknown", repo, cmdruntest.Fail(1, "something unexpected"), Unknown},
		{"unknown: start error", repo, cmdrun.Result{StartErr: os.ErrNotExist}, Unknown},
		{"timeout", repo, cmdrun.Result{CtxErr: context.DeadlineExceeded}, Timeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := cmdruntest.New(t)
			f.On(cmdruntest.MatchName("git")).Return(c.res)
			got, tail := Probe(context.Background(), f.Runner(), c.root, "ssh://h/r", time.Minute)
			if got != c.want {
				t.Fatalf("class %q, want %q (tail %q)", got, c.want, tail)
			}
			if (got == OK) != (tail == "") {
				t.Errorf("tail %q for class %q", tail, got)
			}
		})
	}
}

func TestProbeArgvAndEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/leaked/.git")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Out(""))
	dir := t.TempDir()
	Probe(context.Background(), f.Runner(), dir, "ssh://h/r", time.Minute)
	Probe(context.Background(), f.Runner(), "", "ssh://h/r", time.Minute)
	calls := f.Calls()
	if got := strings.Join(calls[0].Args, " "); got != "-C "+dir+" ls-remote --exit-code ssh://h/r HEAD" {
		t.Errorf("argv %s", got)
	}
	if got := strings.Join(calls[1].Args, " "); got != "ls-remote --exit-code ssh://h/r HEAD" {
		t.Errorf("argv %s", got)
	}
	for _, c := range calls {
		if c.EnvMode != cmdrun.EnvExact || !c.OwnProcGroup {
			t.Errorf("call %+v", c)
		}
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/leaked/.git")
	t.Setenv("GIT_INDEX_FILE", "/leaked/index")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/cfg")
	env := Env()
	has := func(kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			t.Errorf("repository-naming variable leaked: %s", e)
		}
		if strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") {
			n++
		}
	}
	if !has("GIT_TERMINAL_PROMPT=0") || n != 1 {
		t.Errorf("prompt policy: %d entries", n)
	}
	if !has("SSH_AUTH_SOCK=/tmp/agent.sock") || !has("GIT_SSH_COMMAND=ssh -F /tmp/cfg") {
		t.Error("credential-delivery variables must reach the probe")
	}
}

func TestHermetic(t *testing.T) {
	got := Hermetic([]string{"GIT_DIR=/x", "GIT_CONFIG_COUNT=1", "PATH=/usr/bin", "NOEQUALS", "GITHUB_TOKEN=t"})
	if strings.Join(got, " ") != "PATH=/usr/bin NOEQUALS GITHUB_TOKEN=t" {
		t.Errorf("%v", got)
	}
}

func TestTailRedactsThenCuts(t *testing.T) {
	text := strings.Repeat("é", 200) + " https://user:secret@h/r " + strings.Repeat("y ", 200)
	got := tailOf(text)
	if len(got) > maxErrorTail || strings.Contains(got, "secret") {
		t.Errorf("tail %q", got)
	}
	long := tailOf(strings.Repeat("é", 400))
	if len(long) > maxErrorTail || strings.ContainsRune(long, '�') {
		t.Errorf("rune split: %q", long)
	}
	if tailOf("a   b\n c") != "a b c" {
		t.Error("whitespace not collapsed")
	}
}

// TestProbeRealLocalRemote drives the real git against local fixtures only.
func TestProbeRealLocalRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := filepath.Join(tmp, "src")
	for _, args := range [][]string{
		{"init", "-q", src},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Env = Hermetic(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if c, tail := Probe(context.Background(), cmdrun.Exec, "", src, 20*time.Second); c != OK {
		t.Fatalf("local remote: %q %q", c, tail)
	}
	if c, _ := Probe(context.Background(), cmdrun.Exec, "", filepath.Join(tmp, "nope.git"), 20*time.Second); c == OK {
		t.Fatal("missing remote must not be OK")
	}
}
