// testmain_test.go: the in-process double for pg-desk. withHelper swaps Exec
// for a Runner that answers from KEY=value settings, so no test spawns a child
// (under -race every child costs a flat second at exit). The settings keep the
// names the earlier test-helper-process protocol used: it records its argv
// (one per line) to GO_HELPER_ARGS_FILE, answers stdout from
// GO_HELPER_STDOUT_FILE (or GO_HELPER_STDOUT), stderr from GO_HELPER_STDERR and
// exits GO_HELPER_EXIT. The real exec path is covered by runexec_test.go.
package view

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// withHelper swaps Exec for the in-process double configured by env.
func withHelper(t *testing.T, env ...string) {
	t.Helper()
	set := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		set[k] = v
	}
	orig := Exec
	Exec = func(_ context.Context, c Call) CallResult {
		if f := set["GO_HELPER_ARGS_FILE"]; f != "" {
			_ = os.WriteFile(f, []byte(strings.Join(c.Args, "\n")), 0o600)
		}
		var res CallResult
		if f := set["GO_HELPER_STDOUT_FILE"]; f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return CallResult{Stderr: "helper: " + err.Error(), Code: 99}
			}
			res.Stdout = string(b)
		}
		res.Stdout += set["GO_HELPER_STDOUT"]
		res.Stderr = set["GO_HELPER_STDERR"]
		res.Code, _ = strconv.Atoi(set["GO_HELPER_EXIT"])
		return res
	}
	t.Cleanup(func() { Exec = orig })
}
