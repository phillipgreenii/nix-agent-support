// Package cli is the body of both pg-wrap binaries: pg-wrap (run, check, show)
// and pg-wrap-exec. A main package only passes in the process's own arguments,
// environment and streams.
//
// Every failure pg-wrap detects returns Code before the payload starts, with a
// pg-wrap[<stage>]: message on stderr. A successful run never returns: the
// process image is replaced by the composed chain, so nothing of pg-wrap stays
// resident around the payload.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/phillipgreenii/pg-wrap/internal/fail"
	"github.com/phillipgreenii/pg-wrap/internal/spec"
	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

// SpecEnv names the variable pg-wrap-exec reads the spec path from. pg-wrap
// removes it from the payload's environment in every mode.
const SpecEnv = "PG_WRAP_SPEC"

// NoSpecReason is pg-wrap-exec's message when SpecEnv is unset.
const NoSpecReason = "PG_WRAP_SPEC is not set in the environment this launcher received; " +
	"a terminal-multiplexer pane does not inherit your shell's variables"

// Process is what a binary hands in: its arguments (without the program name),
// its environment in os.Environ() form and its streams.
type Process struct {
	Args    []string
	Environ []string
	Stdout  io.Writer
	Stderr  io.Writer
	// Exec replaces the process image. It is syscall.Exec when nil; a test
	// substitutes a recorder so it can inspect what would have been executed.
	Exec func(path string, argv, env []string) error
}

func (p Process) exec() func(string, []string, []string) error {
	if p.Exec != nil {
		return p.Exec
	}
	return syscall.Exec
}

// Main runs the pg-wrap binary and returns its exit code. It returns only on
// failure or after a mode that does not exec (check, show, help).
func Main(p Process) int {
	if len(p.Args) == 0 {
		return report(p, fail.New(fail.StageSpec, "no command given; %s", usage))
	}
	cmd, rest := p.Args[0], p.Args[1:]
	switch cmd {
	case "-h", "--help", "help":
		_, _ = fmt.Fprintln(p.Stdout, helpText)
		return 0
	case "run":
		return runCmd(p, rest)
	case "check":
		return checkCmd(p, rest)
	case "show":
		return showCmd(p, rest)
	default:
		return report(p, fail.New(fail.StageSpec, "unknown command %q; %s", cmd, usage))
	}
}

// ExecMain runs the pg-wrap-exec binary: the spec path comes from SpecEnv and
// every argument, including a leading "run" or "check", is part of the payload.
func ExecMain(p Process) int {
	path := lookupEnv(p.Environ, SpecEnv)
	if path == "" {
		return report(p, fail.New(fail.StageSpec, "%s", NoSpecReason))
	}
	s, err := spec.Load(path)
	if err != nil {
		return report(p, err)
	}
	return report(p, launch(p, s, p.Args))
}

const usage = "usage: pg-wrap run --spec <file> -- <cmd> [args...] | check --spec <file> | show --spec <file>"

const helpText = usage + `

run    read the spec, build the environment, apply the layers and exec the payload
check  decode the spec strictly and verify every referenced path is an executable file
show   print the composed layers without running anything

Every failure pg-wrap detects exits 97 before the payload, with a pg-wrap[<stage>]: message.`

// report prints err (when it is one) and returns Code. A nil error returns 0:
// only the exec path can get here with nil, in a test that records the exec.
func report(p Process, err error) int {
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(p.Stderr, err.Error())
	return fail.Code
}

// parseSpecFlag parses "--spec <file>" and returns the remaining arguments.
func parseSpecFlag(name string, args []string) (string, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	specPath := fs.String("spec", "", "path of the wrap spec")
	if err := fs.Parse(args); err != nil {
		return "", nil, fail.New(fail.StageSpec, "%s: %v; %s", name, err, usage)
	}
	if *specPath == "" {
		return "", nil, fail.New(fail.StageSpec, "%s: --spec <file> is required; %s", name, usage)
	}
	return *specPath, fs.Args(), nil
}

func runCmd(p Process, args []string) int {
	path, rest, err := parseSpecFlag("run", args)
	if err != nil {
		return report(p, err)
	}
	s, err := spec.Load(path)
	if err != nil {
		return report(p, err)
	}
	return report(p, launch(p, s, rest))
}

// launch checks the spec's paths, builds the environment and execs the chain.
// It returns only a failure.
func launch(p Process, s *spec.Spec, args []string) error {
	for _, r := range s.Refs() {
		if r.Err != nil {
			return fail.New(r.Stage, "%s: %v", r.What, r.Err)
		}
	}
	env, err := buildEnv(s, p.Environ)
	if err != nil {
		return err
	}
	argv, err := wrap.Compose(s.Wrap, s.Payload(args))
	if err != nil {
		return fail.New(fail.StageSpec, "%v", err)
	}
	if len(argv) == 0 {
		return fail.New(fail.StageExec, "nothing to run: the spec has no wrap, no exec prefix and no arguments were given")
	}
	path, err := lookPath(argv[0], env)
	if err != nil {
		return fail.New(fail.StageExec, "%v", err)
	}
	if err := p.exec()(path, argv, env); err != nil {
		return fail.New(fail.StageExec, "cannot exec %s: %v", path, err)
	}
	return nil
}

// buildEnv returns the environment the chain starts from: the inherited one, or
// the minimal one plus keep when strict_env is set. SpecEnv is removed in both.
func buildEnv(s *spec.Spec, environ []string) ([]string, error) {
	if err := checkKeep(s.Keep); err != nil {
		return nil, err
	}
	src := environ
	if s.StrictEnv {
		src = wrap.BaseEnv(environ, s.Keep)
	}
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if name, _, ok := strings.Cut(kv, "="); ok && name == SpecEnv {
			continue
		}
		out = append(out, kv)
	}
	return out, nil
}

// checkKeep refuses a keep name that cannot be an environment variable name.
func checkKeep(keep []string) error {
	for i, n := range keep {
		if n == "" || strings.ContainsAny(n, "=\x00") {
			return fail.New(fail.StageEnv, "keep[%d]: %q is not a variable name", i, n)
		}
	}
	return nil
}

// lookPath resolves name against the PATH of env when it has no slash. The
// first argv word is a layer's absolute path, or, for a spec with no layers, the
// payload's own program.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(lookupEnv(env, "PATH")) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%q: executable file not found in PATH", name)
}

// lookupEnv returns the last value of name in an os.Environ()-form slice.
func lookupEnv(environ []string, name string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return v
}

func checkCmd(p Process, args []string) int {
	path, rest, err := parseSpecFlag("check", args)
	if err != nil {
		return report(p, err)
	}
	if len(rest) > 0 {
		return report(p, fail.New(fail.StageSpec, "check: unexpected argument %q; %s", rest[0], usage))
	}
	s, err := spec.Load(path)
	if err != nil {
		return report(p, err)
	}
	var problems []string
	if err := checkKeep(s.Keep); err != nil {
		problems = append(problems, err.Error())
	}
	for _, r := range s.Refs() {
		switch {
		case r.Err != nil:
			problems = append(problems, fail.New(fail.StageSpec, "%s: %v", r.What, r.Err).Error())
		default:
			if msg := spec.PathProblem(r.Path); msg != "" {
				problems = append(problems, fail.New(fail.StageSpec, "%s: %s", r.What, msg).Error())
			}
		}
	}
	for _, line := range problems {
		_, _ = fmt.Fprintln(p.Stderr, line)
	}
	if len(problems) > 0 {
		return fail.Code
	}
	return 0
}

func showCmd(p Process, args []string) int {
	path, rest, err := parseSpecFlag("show", args)
	if err != nil {
		return report(p, err)
	}
	if len(rest) > 0 {
		return report(p, fail.New(fail.StageSpec, "show: unexpected argument %q; %s", rest[0], usage))
	}
	s, err := spec.Load(path)
	if err != nil {
		return report(p, err)
	}
	w := p.Stdout
	_, _ = fmt.Fprintf(w, "schema_version: %d\n", s.SchemaVersion)
	if s.StrictEnv {
		_, _ = fmt.Fprintf(w, "env: minimal, keep: %s\n", keepText(s.Keep))
	} else {
		_, _ = fmt.Fprintln(w, "env: inherited")
	}
	if len(s.Wrap) == 0 {
		_, _ = fmt.Fprintln(w, "layers: none")
	} else {
		_, _ = fmt.Fprintln(w, "layers:")
	}
	for i, e := range s.Wrap {
		layer, err := wrap.Layer(e)
		if err != nil {
			return report(p, fail.New(fail.StageSpec, "wrap[%d]: %v", i, err))
		}
		_, _ = fmt.Fprintf(w, "  %d: %s\n", i, wrap.Display(layer))
	}
	if len(s.Exec) > 0 {
		_, _ = fmt.Fprintf(w, "exec: %s\n", wrap.Display(s.Exec))
	}
	_, _ = fmt.Fprintf(w, "payload: %s\n", payloadText(s))
	return 0
}

func keepText(keep []string) string {
	if len(keep) == 0 {
		return "(none)"
	}
	return strings.Join(keep, " ")
}

func payloadText(s *spec.Spec) string {
	if len(s.Exec) == 0 {
		return "[args...]"
	}
	return wrap.Display(s.Exec) + " [args...]"
}
