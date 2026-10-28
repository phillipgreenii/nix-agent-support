// runexec_test.go: the few tests that cross a real process boundary. Every
// other test in this package answers in process through harness_test.go's
// double, so what only a real child can prove lives here: exit code mapping,
// stdout and stderr capture, the environment a call carries, and a start
// error. They run `sh -c` (hermetic, on every host) and finish in milliseconds.
package apply

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/config"
)

// shAs returns a Runner that runs script under sh in place of whatever binary
// the production code named, through the real ExecRunner. The call's argv is
// $0 $1 ... of the script and its Env rides along unchanged.
func shAs(script string) Runner {
	return func(ctx context.Context, c Call) CallResult {
		return ExecRunner(ctx, Call{Name: "sh", Args: append([]string{"-c", script, c.Name}, c.Args...), Stdin: c.Stdin, Env: c.Env})
	}
}

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

func TestCallMapsARealChildsOutcomeOntoTheConnectorContract(t *testing.T) {
	// ambient must not leak into the absence case.
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", "")
	_ = os.Unsetenv("PG_CONNECTOR_ISSUE_BEADS_DIR")

	args := []string{"issue", "list"}
	cases := []struct {
		name, script string
		cfg          *config.Config
		wantOut      string
		wantErr      string // substring; "" means no error
	}{
		{name: "stdout captured on exit 0", script: `printf '{"result":{}}'`, wantOut: `{"result":{}}`},
		{name: "enveloped error on stdout is the detail", script: `printf '{"error":{"code":"bad","message":"nope"}}'; exit 2`, wantErr: "pg-connector issue list: exit 2: bad: nope"},
		{name: "plain stdout is the detail", script: `printf 'plain words\n'; exit 5`, wantErr: "exit 5: plain words"},
		{name: "stderr is the detail when stdout is empty", script: `printf 'from stderr' >&2; exit 6`, wantErr: "exit 6: from stderr"},
		{name: "exit 4 is not_found", script: `printf 'x' >&2; exit 4`, wantErr: "pg-connector issue list: not_found"},
		{
			name:    "the beads dir variable reaches the child",
			script:  `printf '%s' "$PG_CONNECTOR_ISSUE_BEADS_DIR"`,
			cfg:     &config.Config{BeadsDir: "/tmp/beads"},
			wantOut: "/tmp/beads",
		},
		{
			name:    "no beads dir configured leaves the variable unset",
			script:  `printf '%s' "${PG_CONNECTOR_ISSUE_BEADS_DIR-unset}"`,
			cfg:     &config.Config{},
			wantOut: "unset",
		},
		{name: "argv reaches the child", script: `printf '%s' "$0 $*"`, wantOut: "pg-connector issue list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := call(context.Background(), Env{Runner: shAs(tc.script), Config: tc.cfg}, args)
			if tc.wantErr == "" {
				if err != nil || string(out) != tc.wantOut {
					t.Fatalf("got %q, %v; want %q", out, err, tc.wantOut)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %q, %v; want error containing %q", out, err, tc.wantErr)
			}
		})
	}
}

func TestCallReportsAMissingConnectorAsAnExecError(t *testing.T) {
	missing := func(ctx context.Context, c Call) CallResult {
		c.Name = "pgd-runexec-no-such-binary"
		return ExecRunner(ctx, c)
	}
	_, err := call(context.Background(), Env{Runner: missing}, []string{"issue", "list"})
	var ce *connectorError
	if err == nil || errors.As(err, &ce) || !strings.Contains(err.Error(), "exec pg-connector issue list") {
		t.Fatalf("want an exec error, not a connectorError: %v", err)
	}
}

func TestDeskMapsARealChildsOutcomeOntoTheDeskContract(t *testing.T) {
	env := func(script string) Env { return Env{Runner: shAs(script)} }
	if err := desk(context.Background(), env(`exit 0`), "issue", "refresh", "wb-1"); err != nil {
		t.Fatalf("exit 0: %v", err)
	}
	err := desk(context.Background(), env(`printf 'boom\n' >&2; exit 3`), "issue", "refresh", "wb-1")
	if err == nil || err.Error() != "pg-desk issue refresh wb-1: exit 3: boom" {
		t.Fatalf("stderr detail: %v", err)
	}
	err = desk(context.Background(), env(`exit 9`), "issue", "refresh", "wb-1")
	if err == nil || err.Error() != "pg-desk issue refresh wb-1: exit 9: no diagnostic on stderr" {
		t.Fatalf("empty stderr: %v", err)
	}
	missing := func(ctx context.Context, c Call) CallResult {
		c.Name = "pgd-runexec-no-such-binary"
		return ExecRunner(ctx, c)
	}
	err = desk(context.Background(), Env{Runner: missing}, "issue", "refresh", "wb-1")
	if err == nil || !strings.HasPrefix(err.Error(), "exec pg-desk issue refresh wb-1: ") {
		t.Fatalf("missing binary: %v", err)
	}
}
