//go:build contract

package dtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
)

// Shared setup for the repo's real-bd contract suites (global rule V-3), behind
// the `contract` tag like the suites themselves: a fresh private embedded bd
// store per test, and an environment that cannot reach this machine's shared
// dolt server.

// RealBDDeadline is a hang guard for real bd runs under heavy host load, not a
// performance assertion.
const RealBDDeadline = 3 * time.Minute

// HermeticBDEnv is the process environment minus every BEADS_*, BD_*, GIT_* and
// WORKSPACE_ROOT variable (so nothing can point at the shared dolt server, and
// the GIT_DIR/GIT_INDEX_FILE a commit hook exports cannot make `bd init` see
// this repository's own already-initialized workspace), with
// BEADS_DOLT_AUTO_START=0 pinned. No server is ever started or stopped.
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

// NewRealBD returns a context and a runner rooted at a fresh private embedded
// bd store, whose identity is actor. The test is skipped when bd is not on PATH
// and fails when the store is not the private embedded one.
func NewRealBD(t *testing.T, actor string) (context.Context, *beads.CLIRunner) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	r := &beads.CLIRunner{Dir: dir, Env: HermeticBDEnv(), Actor: actor}
	ctx, cancel := context.WithTimeout(context.Background(), RealBDDeadline)
	t.Cleanup(cancel)
	if out, err := r.Run(ctx, "init", "--prefix", "sx", "--non-interactive", "-q", "--skip-agents", "--skip-hooks"); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
	meta, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read bd metadata: %v", err)
	}
	if !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
		t.Fatalf("bd init did not produce a private embedded store, refusing to continue:\n%s", meta)
	}
	return ctx, r
}
