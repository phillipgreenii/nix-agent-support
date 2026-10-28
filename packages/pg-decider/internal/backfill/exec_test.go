package backfill

import (
	"context"
	"path/filepath"
	"testing"
)

// ExecRunner is the shared x runner; these pin the two behaviours backfill
// relies on: a working directory for the child (the tracker's Dir) and a
// signal told apart from a plain exit code.
func TestExecRunnerRunsTheChildInTheCallsDir(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := ExecRunner(context.Background(), Call{Name: "pwd", Dir: dir})
	got, err := filepath.EvalSymlinks(string(res.Stdout[:len(res.Stdout)-1]))
	if err != nil || got != want || res.Code != 0 {
		t.Fatalf("pwd = %q (%v), want %q; res = %+v", res.Stdout, err, want, res)
	}
}

func TestExecRunnerReportsASignalAsCodeMinusOneWithASignalName(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", `kill -9 $$`}})
	if res.Code != -1 || res.Signal == "" || res.StartErr != nil {
		t.Fatalf("got %+v", res)
	}
	plain := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", `exit 7`}})
	if plain.Code != 7 || plain.Signal != "" {
		t.Fatalf("a plain exit is not a signal: %+v", plain)
	}
}

func TestExecRunnerAppendsEnvToTheAmbientEnvironment(t *testing.T) {
	t.Setenv("PGD_BACKFILL_AMBIENT", "ambient")
	res := ExecRunner(context.Background(), Call{
		Name: "sh", Args: []string{"-c", `printf '%s|%s' "$PGD_BACKFILL_AMBIENT" "$PGD_BACKFILL_ADDED"`},
		Env: []string{"PGD_BACKFILL_ADDED=added"},
	})
	if string(res.Stdout) != "ambient|added" {
		t.Fatalf("got %+v", res)
	}
}
