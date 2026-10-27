// testmain_test.go: the in-process pg-desk double. withHelper swaps the
// Runner seam for a fake that impersonates pg-desk: it records the argv it
// was given (so tests can assert on the exact pg-desk command line, e.g. that
// --cached is never passed), answers with the fixture named by
// GO_HELPER_STDOUT_FILE (or the literal GO_HELPER_STDOUT) on stdout,
// GO_HELPER_STDERR on stderr, and exit code GO_HELPER_EXIT. The vocabulary is
// kept from the earlier re-executed-test-binary double (a race-built child
// sleeps 1 s at exit); nothing here starts a process. The real exec path is
// covered by runner_test.go.
package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fakePgDesk is what withHelper installs; calls is every Call it saw.
type fakePgDesk struct {
	calls []Call
}

// lastArgs is the argv of the most recent call (nil when never called).
func (f *fakePgDesk) lastArgs() []string {
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1].Args
}

// withHelper swaps the Runner seam for an in-process pg-desk double with the
// given GO_HELPER_* behaviour entries.
func withHelper(t *testing.T, env ...string) *fakePgDesk {
	t.Helper()
	cfg := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		cfg[k] = v
	}
	f := &fakePgDesk{}
	orig := runner
	runner = func(_ context.Context, c Call) Result {
		f.calls = append(f.calls, c)
		var res Result
		if file := cfg["GO_HELPER_STDOUT_FILE"]; file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				return Result{Stderr: "helper: " + err.Error(), Code: 99}
			}
			res.Stdout = string(b)
		}
		res.Stdout += cfg["GO_HELPER_STDOUT"]
		res.Stderr = cfg["GO_HELPER_STDERR"]
		res.Code, _ = strconv.Atoi(cfg["GO_HELPER_EXIT"])
		return res
	}
	t.Cleanup(func() { runner = orig })
	return f
}

type buf struct{ b []byte }

func (w *buf) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *buf) String() string              { return string(w.b) }

// runCLI drives the adapter end-to-end and returns what main() would emit.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut buf
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

func mustItems(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON array of objects: %v (stdout=%q)", err, stdout)
	}
	return items
}
