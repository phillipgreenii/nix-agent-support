//go:build integration || smoke

package gate_test

// Shared real-bd/real-git test helpers for create_fleetrace_test.go
// (`//go:build integration`) and lifecycle_smoke_test.go
// (`//go:build smoke`) -- requireBinaries/isolateBeadsEnv/
// initGitRepoWithCommit/runTool/createDeferredBead/inReady (bead pg2-h05lt),
// plus hermeticEnviron (bead pg2-f6cgn) used by runTool below.
//
// Tagged `integration || smoke` rather than left untagged: reachable under
// EITHER `-tags integration` or `-tags smoke` alone (an OR, not an AND), so
// both callers still see it regardless of which non-unit label is selected
// -- but excluded from the plain untagged build, exactly like both of its
// callers already are. Left untagged, this file compiled unconditionally,
// so `golangci-lint run ./...` (no tags; mkGoLint in phillipg-nix-repo-base
// passes none) saw these functions with neither caller compiled alongside
// them and flagged all six "unused" (bead pg2-h8vj7).

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hermeticEnviron returns os.Environ() with the git-hook-injected vars
// (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX, GIT_OBJECT_DIRECTORY,
// GIT_COMMON_DIR) removed. Those vars repoint a test's own tmpdir git/bd
// subprocesses at the REAL repo when the test binary itself runs from inside
// a git commit hook (e.g. this repo's own pre-commit/pre-push test gate) --
// the same hermeticity leak packages/pb/internal/patchid/patchid_test.go had
// before 98f8c95d. Duplicated per-package rather than shared/exported:
// patchid_test.go's copy of this helper is unexported and test-only, so it
// cannot be imported from another package.
func hermeticEnviron() []string {
	skipVars := map[string]bool{
		"GIT_DIR": true, "GIT_INDEX_FILE": true, "GIT_WORK_TREE": true,
		"GIT_PREFIX": true, "GIT_OBJECT_DIRECTORY": true, "GIT_COMMON_DIR": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; !skipVars[k] {
			env = append(env, kv)
		}
	}
	return env
}

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not on PATH", n)
		}
	}
}

// isolateBeadsEnv pins HOME + XDG_* to a temp dir and scrubs the workspace-binding
// vars so real bd creates/uses an isolated EMBEDDED Dolt DB (never the shared
// :25252 server) and git config writes stay in temp. BD_JSON_ENVELOPE=1 matches
// pb's bd client. Deterministic git identity avoids host config dependence.
func isolateBeadsEnv(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	for k, sub := range map[string]string{
		"HOME":            "home",
		"XDG_DATA_HOME":   "data",
		"XDG_STATE_HOME":  "state",
		"XDG_CONFIG_HOME": "cfg",
	} {
		dir := filepath.Join(base, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, dir)
	}
	for _, k := range []string{"BEADS_DIR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT"} {
		t.Setenv(k, "")    // register restore
		_ = os.Unsetenv(k) // truly unset for the test duration
	}
	t.Setenv("BD_JSON_ENVELOPE", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e.com")
}

func initGitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	runTool(t, dir, "git", "init", "-b", "main")
	runTool(t, dir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTool(t, dir, "git", "add", "a.txt")
	runTool(t, dir, "git", "commit", "-m", "c1")
}

// runTool runs name with cwd=dir, inheriting the (isolated) process env, and
// fails the test on a non-zero exit. Returns stdout only (stderr is kept separate
// so JSON output stays clean).
func runTool(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = hermeticEnviron()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\nstderr: %s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func createDeferredBead(t *testing.T, ws, title string) string {
	t.Helper()
	out := runTool(t, ws, "bd", "create", title, "--defer", "2126-01-01", "--json")
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse bd create json: %v\n%s", err, out)
	}
	if env.Data.ID == "" {
		t.Fatalf("bd create returned no id: %s", out)
	}
	return env.Data.ID
}

func inReady(t *testing.T, ws, id string) bool {
	t.Helper()
	out := runTool(t, ws, "bd", "ready", "--json")
	var env struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse bd ready json: %v\n%s", err, out)
	}
	for _, b := range env.Data {
		if b.ID == id {
			return true
		}
	}
	return false
}
