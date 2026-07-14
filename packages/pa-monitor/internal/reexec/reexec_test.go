package reexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAttempt(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want int
	}{
		{"absent", []string{"FOO=bar", "BAZ=1"}, 0},
		{"nil env", nil, 0},
		{"zero", []string{genEnv + "=0"}, 0},
		{"two", []string{genEnv + "=2"}, 2},
		{"surrounded", []string{"FOO=bar", genEnv + "=3", "BAZ=1"}, 3},
		{"malformed abc", []string{genEnv + "=abc"}, 0},
		{"empty value", []string{genEnv + "="}, 0},
		{"negative", []string{genEnv + "=-3"}, 0},
		{"float", []string{genEnv + "=1.5"}, 0},
		// os.Getenv semantics: the FIRST occurrence wins. A child env that
		// accidentally carried a duplicate must not read the trailing copy.
		{"first occurrence wins", []string{genEnv + "=1", genEnv + "=5"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Attempt(tt.env); got != tt.want {
				t.Fatalf("Attempt(%q) = %d, want %d", tt.env, got, tt.want)
			}
		})
	}
}

// genValues extracts every PA_MONITOR_REEXEC_GEN=... value from a child env, in
// order, so a test can assert there is EXACTLY ONE and it holds the right value.
func genValues(env []string) []string {
	prefix := genEnv + "="
	var vals []string
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			vals = append(vals, e[len(prefix):])
		}
	}
	return vals
}

func TestReexecSuccess(t *testing.T) {
	var events []string
	var gotArgv0 string
	var gotArgv, gotEnv []string

	argv0 := "/nix/store/OLDbuild/bin/pa-monitor"
	args := []string{"/nix/store/OLDbuild/bin/pa-monitor", "cmux-bridge", "--flag"}
	env := []string{"FOO=bar", genEnv + "=0", "PATH=/usr/bin"}

	lookPath := func(name string) (string, error) {
		events = append(events, "lookPath:"+name)
		if name != "pa-monitor" {
			t.Errorf("lookPath got %q, want base name %q", name, "pa-monitor")
		}
		// The PATH-resolved target: the darwin-rebuild-flipped profile symlink,
		// deliberately NOT the running build's /nix/store path (argv0).
		return "/etc/profiles/per-user/phillipg/bin/pa-monitor", nil
	}
	execFn := func(a0 string, argv, envv []string) error {
		events = append(events, "exec")
		gotArgv0, gotArgv, gotEnv = a0, argv, envv
		return nil // fake: a real execve never returns on success
	}
	sleep := func(d time.Duration) {
		events = append(events, "sleep:"+d.String())
	}

	if err := Reexec(argv0, args, env, 0, lookPath, execFn, sleep); err != nil {
		t.Fatalf("Reexec returned error: %v", err)
	}

	// backoff pause MUST happen before the exec so MaxAttempts span the
	// activation window rather than firing back-to-back in milliseconds.
	wantOrder := []string{"sleep:" + backoff.String(), "lookPath:pa-monitor", "exec"}
	if strings.Join(events, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("event order = %v, want %v", events, wantOrder)
	}

	// argv0 for execve is the PATH-resolved target (picks up the new build).
	if gotArgv0 != "/etc/profiles/per-user/phillipg/bin/pa-monitor" {
		t.Fatalf("exec argv0 = %q, want resolved PATH target", gotArgv0)
	}
	// argv is the ORIGINAL os.Args, unchanged (argv[0] pinned to os.Args[0]).
	if strings.Join(gotArgv, "|") != strings.Join(args, "|") {
		t.Fatalf("exec argv = %v, want original args %v", gotArgv, args)
	}
	// Exactly one GEN entry, incremented to attempt+1.
	if vals := genValues(gotEnv); len(vals) != 1 || vals[0] != "1" {
		t.Fatalf("child GEN values = %v, want exactly one %q", vals, "1")
	}
	// Unrelated env survives.
	if !slices.Contains(gotEnv, "FOO=bar") || !slices.Contains(gotEnv, "PATH=/usr/bin") {
		t.Fatalf("child env dropped unrelated vars: %v", gotEnv)
	}
}

func TestReexecReplaceGenInPlace(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		attempt int
		wantVal string
	}{
		{"absent -> appended", []string{"FOO=bar"}, 0, "1"},
		{"stale replaced", []string{"FOO=bar", genEnv + "=2", "BAZ=1"}, 2, "3"},
		// A duplicate key must collapse to exactly one at the new value; leaving
		// a stale first copy would make the child's os.Getenv read the wrong gen.
		{"duplicate collapsed", []string{genEnv + "=2", genEnv + "=2"}, 2, "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotEnv []string
			execFn := func(_ string, _, envv []string) error {
				gotEnv = envv
				return nil
			}
			lookPath := func(string) (string, error) { return "/abs/pa-monitor", nil }
			if err := Reexec("/x/pa-monitor", []string{"/x/pa-monitor"}, tt.env, tt.attempt, lookPath, execFn, func(time.Duration) {}); err != nil {
				t.Fatalf("Reexec error: %v", err)
			}
			vals := genValues(gotEnv)
			if len(vals) != 1 {
				t.Fatalf("want exactly one %s entry, got %v", genEnv, vals)
			}
			if vals[0] != tt.wantVal {
				t.Fatalf("gen = %q, want %q", vals[0], tt.wantVal)
			}
		})
	}
}

func TestReexecLookPathError(t *testing.T) {
	execCalled := false
	execFn := func(string, []string, []string) error { execCalled = true; return nil }
	lookPath := func(string) (string, error) { return "", exec.ErrNotFound }
	err := Reexec("/x/pa-monitor", []string{"/x/pa-monitor"}, nil, 0, lookPath, execFn, func(time.Duration) {})
	if err == nil {
		t.Fatal("want error when lookPath fails, got nil")
	}
	if execCalled {
		t.Fatal("execFn must NOT be called when lookPath fails")
	}
}

func TestReexecLookPathNonAbsolute(t *testing.T) {
	execCalled := false
	execFn := func(string, []string, []string) error { execCalled = true; return nil }
	// A relative resolution is a fail-safe guard: execve must get an absolute path.
	lookPath := func(string) (string, error) { return "pa-monitor", nil }
	err := Reexec("/x/pa-monitor", []string{"/x/pa-monitor"}, nil, 0, lookPath, execFn, func(time.Duration) {})
	if err == nil {
		t.Fatal("want error when lookPath returns a non-absolute path, got nil")
	}
	if execCalled {
		t.Fatal("execFn must NOT be called for a non-absolute resolution")
	}
}

func TestReexecExecFnError(t *testing.T) {
	wantErr := fmt.Errorf("boom")
	execFn := func(string, []string, []string) error { return wantErr }
	lookPath := func(string) (string, error) { return "/abs/pa-monitor", nil }
	err := Reexec("/x/pa-monitor", []string{"/x/pa-monitor"}, nil, 0, lookPath, execFn, func(time.Duration) {})
	if err == nil {
		t.Fatal("want the execFn error propagated so the caller gives up")
	}
}

// --- E2E: exercise the REAL syscall.Exec once via a helper subprocess. ---
//
// The parent builds a tiny stub binary named "pa-monitor", then re-invokes THIS
// test binary as a helper (TestReexecHelperProcess). The helper calls the real
// Reexec (with the real sysExec) which execve-replaces the helper process image
// with the stub. The stub prints its argv + selected env and exits 0. The parent
// asserts args/env survived the execve. We deliberately never syscall.Exec the
// test binary itself (that never returns and would re-run the whole suite).
func TestReexecExecvePreservesArgsEnv(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "reexec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Build the stub, named "pa-monitor" so filepath.Base(argv0) == "pa-monitor".
	stubSrc := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(stubSrc, []byte(stubProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	stubBin := filepath.Join(dir, "pa-monitor")
	if out, err := exec.Command("go", "build", "-o", stubBin, stubSrc).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestReexecHelperProcess")
	cmd.Env = append(
		os.Environ(),
		"GO_WANT_REEXEC_HELPER=1",
		"REEXEC_STUB_DIR="+dir,
		"REEXEC_SENTINEL=hello-world",
		genEnv+"=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper subprocess failed: %v\n%s", err, out)
	}
	got := string(out)

	// argv[0] pinned to "pa-monitor" (the original args[0]), then the subcommand.
	if !strings.Contains(got, "ARGV=pa-monitor|cmux-bridge|--flag") {
		t.Errorf("argv not preserved across execve; output:\n%s", got)
	}
	// GEN incremented to attempt+1 = 1.
	if !strings.Contains(got, "GEN=1") {
		t.Errorf("GEN not set to attempt+1; output:\n%s", got)
	}
	// Unrelated env preserved.
	if !strings.Contains(got, "SENTINEL=hello-world") {
		t.Errorf("env not preserved across execve; output:\n%s", got)
	}
}

// TestReexecHelperProcess is not a real test: it is the child half of
// TestReexecExecvePreservesArgsEnv. It runs the real Reexec (real sysExec) which
// should execve into the stub and never return here.
func TestReexecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REEXEC_HELPER") != "1" {
		t.Skip("helper process; only runs when re-invoked by the E2E parent")
	}
	dir := os.Getenv("REEXEC_STUB_DIR")
	argv0 := filepath.Join(dir, "pa-monitor")
	args := []string{"pa-monitor", "cmux-bridge", "--flag"}
	lookPath := func(name string) (string, error) {
		return filepath.Join(dir, name), nil // absolute stub path
	}
	// no-op sleep to keep the E2E fast
	err := Reexec(argv0, args, os.Environ(), 0, lookPath, sysExec, func(time.Duration) {})
	// Reaching here means execve failed. Report and exit non-zero so the parent
	// sees the failure instead of a silent replacement.
	fmt.Fprintf(os.Stdout, "REEXEC_RETURNED err=%v\n", err)
	os.Exit(7)
}

// stubProgram is a stdlib-only main that echoes its argv and two env vars so the
// parent can verify they survived execve, then exits 0.
const stubProgram = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println("ARGV=" + strings.Join(os.Args, "|"))
	fmt.Println("GEN=" + os.Getenv("` + "PA_MONITOR_REEXEC_GEN" + `"))
	fmt.Println("SENTINEL=" + os.Getenv("REEXEC_SENTINEL"))
	os.Exit(0)
}
`
