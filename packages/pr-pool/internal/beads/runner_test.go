package beads

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestScrubbedEnv_removesBeadsTaint(t *testing.T) {
	t.Setenv("BEADS_DIR", "/wrong/.beads")
	t.Setenv("WORKSPACE_ROOT", "/wrong")
	t.Setenv("PATH", "/usr/bin")
	r := NewCLIRunnerForRepo("/repo")
	if r.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", r.Dir)
	}
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			t.Errorf("scrubbed env still contains %q", kv)
		}
	}
	var sawPath bool
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "PATH=") {
			sawPath = true
		}
	}
	if !sawPath {
		t.Error("scrubbed env dropped PATH; should only remove BEADS_DIR/WORKSPACE_ROOT")
	}
}

func TestScrubEnv_pure(t *testing.T) {
	in := []string{"A=1", "BEADS_DIR=/x", "B=2", "WORKSPACE_ROOT=/y", "C=3"}
	got := scrubEnv(in)
	want := []string{"A=1", "B=2", "C=3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scrubEnv = %v, want %v", got, want)
	}
	_ = os.Environ // referenced to keep import if test is trimmed
}

// fakeRunner returns canned stdout/err without spawning bd. Reused by issue_test.go.
type fakeRunner struct {
	out  string
	err  error
	args [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.args = append(f.args, args)
	return f.out, f.err
}

// compile-time check: fakeRunner satisfies Runner
var _ Runner = (*fakeRunner)(nil)
