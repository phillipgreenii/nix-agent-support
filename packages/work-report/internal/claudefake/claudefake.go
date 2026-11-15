// Package claudefake installs a fake claude executable for one test.
//
// It follows the exec'd-CLI test-double convention of internal/pgconn/fake: a
// small shell script is written into a temp dir and that dir is put first on
// PATH, so the code under test runs its real exec path against canned output.
// Unlike that double it also records what the process read on stdin. Because
// it is a plain (non-_test) file other packages' tests can import it; it is
// test-only by convention and imports testing.
package claudefake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Behavior is what the fake claude does on every invocation: print Stdout and
// Stderr, then exit with Exit.
type Behavior struct {
	Stdout, Stderr string
	Exit           int
}

// Call is one recorded invocation: the exact argv (without the program name;
// an empty-string argument is preserved) and the bytes read from stdin.
type Call struct {
	Args  []string
	Stdin string
}

// Recorder reads back what the fake received.
type Recorder struct {
	callsPath string
}

// argSep ends every recorded argument, stdinSep ends the argument list of one
// call, and callSep ends every recorded call; a newline cannot be a terminator
// because an argument or stdin may contain one.
const (
	argSep   = "\x1f"
	stdinSep = "\x1d"
	callSep  = "\x1e"
)

// Install writes a fake claude with behavior b, puts it first on PATH for the
// rest of the test and returns a Recorder of its calls.
func Install(t *testing.T, b Behavior) *Recorder {
	t.Helper()
	dir := t.TempDir()
	callsPath := filepath.Join(dir, "calls")

	var s strings.Builder
	s.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&s, "for a in \"$@\"; do printf '%%s\\037' \"$a\"; done >> %s\n", quote(callsPath))
	fmt.Fprintf(&s, "printf '\\035' >> %s\n", quote(callsPath))
	fmt.Fprintf(&s, "cat >> %s\n", quote(callsPath))
	fmt.Fprintf(&s, "printf '\\036' >> %s\n", quote(callsPath))
	fmt.Fprintf(&s, "printf '%%s' %s\n", quote(b.Stdout))
	fmt.Fprintf(&s, "printf '%%s' %s >&2\n", quote(b.Stderr))
	fmt.Fprintf(&s, "exit %d\n", b.Exit)

	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(s.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(callsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Recorder{callsPath: callsPath}
}

// Calls returns every invocation the fake received, in order.
func (r *Recorder) Calls() []Call {
	raw, err := os.ReadFile(r.callsPath)
	if err != nil {
		return nil
	}
	var out []Call
	for _, rec := range strings.Split(string(raw), callSep) {
		if rec == "" {
			continue
		}
		argPart, stdin, _ := strings.Cut(rec, stdinSep)
		var args []string
		if argPart != "" {
			// each arg is followed by the separator, so the last element is empty.
			args = strings.Split(argPart, argSep)
			args = args[:len(args)-1]
		}
		out = append(out, Call{Args: args, Stdin: stdin})
	}
	return out
}

// quote single-quotes s for a POSIX shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Fake is Install without the process: the same Behavior served in-process
// through the code under test's run seam, recording the same Calls. A test
// that does not need the real exec path uses it so each call costs no process
// launch.
type Fake struct {
	b Behavior

	mu    sync.Mutex
	calls []Call
}

// NewFake returns an in-process fake claude with behavior b.
func NewFake(b Behavior) *Fake { return &Fake{b: b} }

// Run runs the fake claude like exec.Cmd.Run would: a done context fails
// before anything is recorded, a non-zero Exit fails with the text an
// *exec.ExitError has ("exit status N"), and the output is returned either
// way. path and dir are ignored.
func (f *Fake) Run(ctx context.Context, _, _ string, args []string, stdin string) (stdout, stderr string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	f.mu.Lock()
	f.calls = append(f.calls, Call{Args: slices.Clone(args), Stdin: stdin})
	f.mu.Unlock()
	if f.b.Exit != 0 {
		err = fmt.Errorf("exit status %d", f.b.Exit)
	}
	return f.b.Stdout, f.b.Stderr, err
}

// Calls returns every invocation the fake received, in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
