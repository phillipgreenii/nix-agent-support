// Package fake installs a fake pg-connector executable for one test.
//
// It follows the exec'd-CLI test-double convention: a small shell script is
// written into a temp dir and that dir is put first on PATH, so the code under
// test runs its real exec path against canned output. Routes are matched by
// argv prefix, first match wins. Because it is a plain (non-_test) file other
// packages' tests can import it; it is test-only by convention and imports
// testing.
package fake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Route is one canned response. Match is an argv prefix, e.g.
// {"activity", "list"}; an empty Match matches every call. A non-zero Exit
// also makes the fake print a one-line message on stderr.
type Route struct {
	Match  []string
	Stdout string
	Exit   int
}

// Recorder reads back what the fake received.
type Recorder struct {
	callsPath string
}

const argSep = "\x1f"

// Install writes a fake pg-connector implementing routes, puts it first on
// PATH for the rest of the test and returns a Recorder of its calls. A call
// that matches no route prints a message on stderr and exits 64.
func Install(t *testing.T, routes ...Route) *Recorder {
	t.Helper()
	dir := t.TempDir()
	callsPath := filepath.Join(dir, "calls")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "for a in \"$@\"; do printf '%%s\\037' \"$a\"; done >> %s\n", quote(callsPath))
	fmt.Fprintf(&b, "printf '\\n' >> %s\n", quote(callsPath))
	for _, r := range routes {
		var cond []string
		cond = append(cond, fmt.Sprintf("[ \"$#\" -ge %d ]", len(r.Match)))
		for i, m := range r.Match {
			cond = append(cond, fmt.Sprintf("[ \"$%d\" = %s ]", i+1, quote(m)))
		}
		fmt.Fprintf(&b, "if %s; then\n", strings.Join(cond, " && "))
		fmt.Fprintf(&b, "  printf '%%s' %s\n", quote(r.Stdout))
		if r.Exit != 0 {
			fmt.Fprintf(&b, "  printf '%%s\\n' %s >&2\n", quote(fmt.Sprintf("fake pg-connector: simulated failure (exit %d)", r.Exit)))
		}
		fmt.Fprintf(&b, "  exit %d\nfi\n", r.Exit)
	}
	b.WriteString("printf '%s\\n' 'fake pg-connector: no route for this call' >&2\nexit 64\n")

	if err := os.WriteFile(filepath.Join(dir, "pg-connector"), []byte(b.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(callsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Recorder{callsPath: callsPath}
}

// Calls returns every argv (without the program name) the fake received, in
// order.
func (r *Recorder) Calls() [][]string {
	raw, err := os.ReadFile(r.callsPath)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		args := strings.Split(line, argSep)
		// each arg is followed by the separator, so the last element is empty.
		out = append(out, args[:len(args)-1])
	}
	return out
}

// quote single-quotes s for a POSIX shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
