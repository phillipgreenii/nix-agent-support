// testmain_test.go: in-process double for pg-desk (see
// internal/view/testmain_test.go for the settings it understands). No test
// here spawns a child: under -race every child costs a flat second at exit.
package main

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// withHelper swaps view.Exec for an in-process double configured by env
// (GO_HELPER_ARGS_FILE, GO_HELPER_STDOUT_FILE, GO_HELPER_STDOUT,
// GO_HELPER_STDERR, GO_HELPER_EXIT).
func withHelper(t *testing.T, env ...string) {
	t.Helper()
	set := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		set[k] = v
	}
	orig := view.Exec
	view.Exec = func(_ context.Context, c view.Call) view.CallResult {
		if f := set["GO_HELPER_ARGS_FILE"]; f != "" {
			_ = os.WriteFile(f, []byte(strings.Join(c.Args, "\n")), 0o600)
		}
		var res view.CallResult
		if f := set["GO_HELPER_STDOUT_FILE"]; f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return view.CallResult{Stderr: []byte("helper: " + err.Error()), Code: 99}
			}
			res.Stdout = b
		}
		res.Stdout = append(res.Stdout, set["GO_HELPER_STDOUT"]...)
		res.Stderr = []byte(set["GO_HELPER_STDERR"])
		res.Code, _ = strconv.Atoi(set["GO_HELPER_EXIT"])
		return res
	}
	t.Cleanup(func() { view.Exec = orig })
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut bytes.Buffer
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The existing tests of plan and apply exercise the rules and the writes, not
// the work-item lookup; they run with none (lookupRead answers nil, nil, which
// production never does). lookup_test.go swaps the real reader back in.
func init() {
	lookupRead = func(context.Context, apply.Env) (*apply.Lookup, error) { return nil, nil }
}
