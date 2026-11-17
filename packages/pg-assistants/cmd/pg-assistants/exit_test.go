package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
)

func runExit(t *testing.T, vars map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	tc := newTestCLI(t)
	tc.vars = vars
	code = tc.main(t.Context(), append([]string{"exit"}, args...))
	return code, tc.out.String(), tc.errb.String()
}

func TestExitWritesRequest(t *testing.T) {
	state := t.TempDir()
	code, stdout, stderr := runExit(t, map[string]string{"PG_ASSISTANT_ID": "example-a", "PG_ASSISTANTS_STATE_DIR": state})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if want := "exit requested for example-a; end your turn now\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	ok, err := supervisor.HasExitRequest(state, "example-a")
	if err != nil || !ok {
		t.Errorf("HasExitRequest = %v, %v; want true", ok, err)
	}
}

func TestExitWithoutAssistantID(t *testing.T) {
	state := t.TempDir()
	code, stdout, stderr := runExit(t, map[string]string{"PG_ASSISTANTS_STATE_DIR": state})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if want := "PG_ASSISTANT_ID is not set: run this from inside a pg-assistants session"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	assertEmptyDir(t, state)
}

func TestExitRejectsBadID(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	mkdir(t, state)
	for _, id := range []string{"../x", "Example", "a/b", "x;"} {
		t.Run(id, func(t *testing.T) {
			code, _, stderr := runExit(t, map[string]string{"PG_ASSISTANT_ID": id, "PG_ASSISTANTS_STATE_DIR": state})
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, "is not a valid id") {
				t.Errorf("stderr = %q, want it to say the id is not valid", stderr)
			}
			assertEmptyDir(t, state)
			if _, err := os.Stat(filepath.Join(filepath.Dir(state), "x")); err == nil {
				t.Errorf("a file was written outside the state dir")
			}
		})
	}
}

func TestExitNeedsAbsoluteStateDir(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"unset":    {"PG_ASSISTANT_ID": "example-a"},
		"relative": {"PG_ASSISTANT_ID": "example-a", "PG_ASSISTANTS_STATE_DIR": "state"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runExit(t, vars)
			if code != 2 || !strings.Contains(stderr, "PG_ASSISTANTS_STATE_DIR") {
				t.Errorf("exit = %d, stderr %q; want 2 naming PG_ASSISTANTS_STATE_DIR", code, stderr)
			}
		})
	}
}

func TestExitUsage(t *testing.T) {
	vars := map[string]string{"PG_ASSISTANT_ID": "example-a", "PG_ASSISTANTS_STATE_DIR": t.TempDir()}
	if code, _, _ := runExit(t, vars, "extra"); code != 2 {
		t.Errorf("exit extra = %d, want 2", code)
	}
	if code, stdout, _ := runExit(t, vars, "--help"); code != 0 || !strings.Contains(stdout, "pg-assistants exit") {
		t.Errorf("exit --help = %d, %q; want 0 and the help", code, stdout)
	}
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s holds %d entries, want none (%v)", dir, len(entries), entries)
	}
}
