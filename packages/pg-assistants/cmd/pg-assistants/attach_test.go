package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

// attachFixture is a config whose state dir is a temp dir, and a test CLI.
func attachFixture(t *testing.T) (tc *testCLI, configPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	configPath = tempConfig(t, pathsTOML(stateDir, filepath.Join(dir, ".claude.json"))+assistantTOML("example-a", "/example/work/a"))
	return newTestCLI(t), configPath, stateDir
}

func TestAttachArgvAndEnv(t *testing.T) {
	tc, configPath, stateDir := attachFixture(t)
	answerTools(tc, newFakeTmux("example-a", "example-b"))
	tc.environv = []string{"HOME=/example/home", "TMUX=/private/tmp/tmux-501/default,123,0", "PATH=/usr/bin:/bin", "TMUXX=kept"}
	code := tc.main(t.Context(), []string{"attach", "example-a", "--config", configPath})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, tc.errb.String())
	}
	execs := tc.execCalls()
	if len(execs) != 1 {
		t.Fatalf("exec called %d times, want 1", len(execs))
	}
	e := execs[0]
	wantArgv := []string{tmuxBin, "-L", "pg-assistants", "-f", filepath.Join(stateDir, "tmux.conf"), "attach", "-t", "=example-a"}
	if e.argv0 != tmuxBin || !slices.Equal(e.argv, wantArgv) {
		t.Errorf("exec(%q, %q), want exec(%q, %q)", e.argv0, e.argv, tmuxBin, wantArgv)
	}
	wantEnv := []string{"HOME=/example/home", "PATH=/usr/bin:/bin", "TMUXX=kept"}
	if !slices.Equal(e.env, wantEnv) {
		t.Errorf("exec env = %q, want %q (TMUX removed, the rest kept in order)", e.env, wantEnv)
	}
}

func TestAttachFlagBeforeID(t *testing.T) {
	tc, configPath, _ := attachFixture(t)
	answerTools(tc, newFakeTmux("example-a"))
	if code := tc.main(t.Context(), []string{"attach", "--config", configPath, "example-a"}); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, tc.errb.String())
	}
	if n := len(tc.execCalls()); n != 1 {
		t.Errorf("exec called %d times, want 1", n)
	}
}

func TestAttachExitCodes(t *testing.T) {
	cases := []struct {
		name       string
		answer     func(tc *testCLI)
		id         string
		want       int
		wantStderr string
	}{
		{"no session", func(tc *testCLI) { answerTools(tc, newFakeTmux("example-b")) }, "example-a", 4, "pg-assistants status"},
		{"no server", func(tc *testCLI) { answerTools(tc, newFakeTmux()) }, "example-a", 3, "no tmux server"},
		{"tmux fails", func(tc *testCLI) {
			tc.fake.On(cmdruntest.MatchName(tmuxBin)).Return(cmdruntest.Fail(1, "permission denied"))
		}, "example-a", 3, "permission denied"},
		// The id is checked before anything runs: the fake answers no call.
		{"invalid id", func(*testCLI) {}, "../x", 2, "not a valid assistant id"},
		{"upper-case id", func(*testCLI) {}, "Example-A", 2, "not a valid assistant id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc, configPath, _ := attachFixture(t)
			c.answer(tc)
			code := tc.main(t.Context(), []string{"attach", c.id, "--config", configPath})
			if code != c.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, c.want, tc.errb.String())
			}
			if !strings.Contains(tc.errb.String(), c.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", tc.errb.String(), c.wantStderr)
			}
			if n := len(tc.execCalls()); n != 0 {
				t.Errorf("exec called %d times, want 0", n)
			}
		})
	}
}

func TestAttachUsageAndConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no id", []string{"attach"}, 2},
		{"two ids", []string{"attach", "example-a", "example-b"}, 2},
		{"missing config", []string{"attach", "example-a", "--config", missing}, 7},
		{"help", []string{"attach", "--help"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, stdout, stderr := runCLI(t, c.args...); code != c.want {
				t.Errorf("exit = %d, want %d (stdout %q, stderr %q)", code, c.want, stdout, stderr)
			}
		})
	}
}
