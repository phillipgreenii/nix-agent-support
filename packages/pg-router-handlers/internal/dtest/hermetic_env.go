package dtest

import (
	"os"
	"strings"
)

// HermeticBDEnv is the process environment minus every BEADS_*, BD_*, GIT_* and
// WORKSPACE_ROOT variable (so nothing can point at the shared dolt server, and
// the GIT_DIR/GIT_INDEX_FILE a commit hook exports cannot make `bd init` see
// this repository's own already-initialized workspace), with
// BEADS_DOLT_AUTO_START=0 pinned. No server is ever started or stopped.
//
// It lives outside the `contract`-tagged real-bd setup so its unit test runs in
// the default tier on every `go test`, not only in the commit-time contract hook.
func HermeticBDEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BEADS_") || strings.HasPrefix(kv, "BD_") || strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			continue
		}
		env = append(env, kv)
	}
	return append(
		env,
		"BEADS_DOLT_AUTO_START=0",
		"BD_NON_INTERACTIVE=1",
		"BD_JSON_ENVELOPE=1",
	)
}
