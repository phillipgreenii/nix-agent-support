//go:build unix

package runner

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Exec is the process-execution path validate mode reuses (it takes no run
// directory), so it is tested on its own against the real fake generator.
func TestExecRunsAnArgvInADirectoryWithTheGivenEnvironment(t *testing.T) {
	fx := newFixture(t)
	g := fx.gen(script{Stdout: `{"hello":true}`, Stderr: "warn\n", Exit: 3})
	dir := t.TempDir()
	res := fx.runner.Exec(context.Background(), ExecSpec{
		Argv:  g.Command,
		Dir:   dir,
		Env:   fx.runner.Environ(g, ModeValidate),
		Stdin: []byte("in"),
	})
	if res.StartErr != nil || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("result = %+v", res)
	}
	if string(res.Stdout) != `{"hello":true}` || res.Stderr != "warn\n" {
		t.Fatalf("stdout %q stderr %q", res.Stdout, res.Stderr)
	}
	if res.StdoutCapped || res.TimedOut || res.Cancelled || res.Signal != "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestExecReportsAMissingExecutable(t *testing.T) {
	fx := newFixture(t)
	res := fx.runner.Exec(context.Background(), ExecSpec{Argv: []string{"/nonexistent/generator"}, Dir: os.TempDir()})
	if !res.NotFound || res.StartErr == nil || res.ExitCode != nil {
		t.Fatalf("result = %+v", res)
	}
	res = fx.runner.Exec(context.Background(), ExecSpec{Argv: nil})
	if !res.NotFound {
		t.Fatalf("empty argv: %+v", res)
	}
}

func TestExecTerminatesAtTheTimeout(t *testing.T) {
	fx := newFixture(t)
	g := fx.gen(script{Hang: true})
	res := fx.runner.Exec(context.Background(), ExecSpec{Argv: g.Command, Dir: t.TempDir(), Env: fx.runner.Environ(g, ModeGenerate), Timeout: 1_500_000_000})
	if !res.TimedOut || res.Cancelled || res.Signal != "SIGTERM" {
		t.Fatalf("result = %+v", res)
	}
	if strings.TrimSpace(res.Stderr) != "" {
		t.Fatalf("stderr = %q", res.Stderr)
	}
}
