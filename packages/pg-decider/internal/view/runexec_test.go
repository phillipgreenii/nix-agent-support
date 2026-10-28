// runexec_test.go: the few tests that cross a real process boundary. Every
// other test answers in process through withHelper (testmain_test.go), so what
// only a real child can prove lives here: exit code mapping, stdout and stderr
// capture, and the environment a call carries. They run `sh -c` (hermetic, on
// every host) and finish in milliseconds. A pg-desk that cannot be started is
// covered by TestReadWhenPgDeskCannotBeStarted.
package view

import (
	"context"
	"strings"
	"testing"
)

func TestExecRunnerCapturesStdoutStderrAndExitCode(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", `printf out; printf err >&2; exit 7`}})
	if string(res.Stdout) != "out" || string(res.Stderr) != "err" || res.Code != 7 || res.Signal != "" || res.StartErr != nil {
		t.Fatalf("got %+v", res)
	}
	if ok := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "exit 0"}}); ok.Code != 0 || ok.StartErr != nil {
		t.Fatalf("exit 0: got %+v", ok)
	}
}

func TestExecRunnerAppendsEnvToTheAmbientEnvironmentAndFeedsStdin(t *testing.T) {
	t.Setenv("PGD_RUNEXEC_AMBIENT", "ambient")
	res := ExecRunner(context.Background(), Call{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s|%s|' "$PGD_RUNEXEC_AMBIENT" "$PGD_RUNEXEC_ADDED"; cat`},
		Stdin: []byte("in"),
		Env:   []string{"PGD_RUNEXEC_ADDED=added"},
	})
	if string(res.Stdout) != "ambient|added|in" || res.Code != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestExecRunnerReportsASignalAsCodeMinusOne(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", `kill -9 $$`}})
	if res.Code != -1 || res.Signal == "" || res.StartErr != nil {
		t.Fatalf("got %+v", res)
	}
}

func TestExecRunnerMissingBinaryIsAStartErrorNotAnExitCode(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "pgd-runexec-no-such-binary"})
	if res.StartErr == nil || res.Code != -1 {
		t.Fatalf("got %+v", res)
	}
}

// TestReadMapsARealChildsOutcome runs Read through the real ExecRunner, with sh
// standing in for pg-desk.
func TestReadMapsARealChildsOutcome(t *testing.T) {
	asSh := func(script string) {
		t.Helper()
		orig := Exec
		t.Cleanup(func() { Exec = orig })
		Exec = func(ctx context.Context, c Call) CallResult {
			return ExecRunner(ctx, Call{Name: "sh", Args: append([]string{"-c", script, c.Name}, c.Args...)})
		}
	}

	asSh(`printf '%s' "$0 $*" >&2; exit 3`)
	_, err := Read(context.Background(), "pr", "acme/widgets#42")
	if err == nil || !strings.Contains(err.Error(), "pg-desk pr show acme/widgets#42 exited 3: pg-desk pr show acme/widgets#42 --json") {
		t.Fatalf("stderr detail and argv: %v", err)
	}

	asSh(`exit 9`)
	_, err = Read(context.Background(), "pr", "x")
	if err == nil || !strings.Contains(err.Error(), "exited 9: no diagnostic on stderr") {
		t.Fatalf("empty stderr: %v", err)
	}

	asSh(`printf '%s' '{"contract":"pg-desk.view/v1","type":"pr","id":"x","snapshot":null,"links":[]}'`)
	v, err := Read(context.Background(), "pr", "x")
	if err != nil || v == nil || v.ID != "x" {
		t.Fatalf("stdout parse: %v %v", v, err)
	}
}
