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

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// hermeticEnviron is the environment every real bd/git/pn/pb child of these
// tests runs with (beads pg2-f6cgn, pg2-rxs4j). It is an ALLOWLIST over the
// process environment (hermeticEnvFrom), never a scrub: an enumerated deny-list
// of GIT_* names is only as complete as its author's memory, and the code-file
// standards forbid it. Anything that selects a git repository (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_COMMON_DIR, ...) or a beads workspace
// (BEADS_*, WORKSPACE_ROOT, ...) is dropped by construction, so a test binary
// run from inside a git commit hook cannot repoint its tmpdir git/bd children at
// the REAL repository.
func hermeticEnviron() []string { return hermeticEnvFrom(os.Environ()) }

// hermeticEnvFrom builds the child environment from parent: only the names in
// hermeticPassthrough (and the LC_*/NIX_* families) survive, plus
// GIT_CONFIG_NOSYSTEM=1 and BEADS_DOLT_AUTO_START=0 (bd must never spawn its own
// dolt server). The names that survive are the ones isolateBeadsEnv
// (and the smoke setup) deliberately set for the child: the temp HOME/XDG_*,
// BD_JSON_ENVELOPE, the git identity and PN_WORKSPACE_ROOT.
func hermeticEnvFrom(parent []string) []string {
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")

		if hermeticPassthrough[k] || strings.HasPrefix(k, "LC_") || strings.HasPrefix(k, "NIX_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1", "BEADS_DOLT_AUTO_START=0")
}

// hermeticPassthrough is the complete list of parent-environment names a child
// may inherit. Adding a name is a deliberate decision; it MUST NOT be a name that
// selects a git repository or a beads workspace.
var hermeticPassthrough = map[string]bool{
	// executables, scratch, locale, user
	"PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TZ": true, "TERM": true, "SSL_CERT_FILE": true,
	// set by isolateBeadsEnv for the child
	"HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "XDG_CONFIG_HOME": true,
	"BD_JSON_ENVELOPE": true, "BD_NON_INTERACTIVE": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	// set by the smoke setup for pn
	"PN_WORKSPACE_ROOT": true,
}

// goBuildEnviron is the environment for the one child that needs the toolchain
// state of the parent (go build): the hermetic base plus the Go variables. It
// still carries no GIT_* repo selector, so Go's VCS stamping cannot be
// redirected at an outer repository. Call it before isolateBeadsEnv so HOME is
// the real one (GOCACHE and GOPATH default under it).
func goBuildEnviron() []string {
	env := hermeticEnvFrom(os.Environ())
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); goBuildPassthrough[k] {
			env = append(env, kv)
		}
	}
	return env
}

var goBuildPassthrough = map[string]bool{
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOFLAGS": true,
	"GOPROXY": true, "GONOPROXY": true, "GOSUMDB": true, "GONOSUMDB": true,
	"GOPRIVATE": true, "GOTOOLCHAIN": true, "GOROOT": true, "GOENV": true,
	"GOWORK": true, "GOOS": true, "GOARCH": true, "CGO_ENABLED": true,
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
	scrub := []string{"BEADS_DIR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT"}
	// Every other BEADS_* variable goes too (BEADS_DOLT_SERVER_HOST/PORT,
	// BEADS_DOLT_SHARED_SERVER, BEADS_ACTOR, ...): the ambient environment of a
	// drain session points bd at the live tracker, and any of these could do the
	// same for the fixture.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "BEADS_") {
			scrub = append(scrub, k)
		}
	}
	for _, k := range scrub {
		t.Setenv(k, "")    // register restore
		_ = os.Unsetenv(k) // truly unset for the test duration
	}
	t.Setenv("BD_JSON_ENVELOPE", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e.com")
}

// initGitRepoWithCommit returns the directory of a hermetic x/gittest
// repository holding one commit ("c1" adding a.txt on branch main). The
// fixture owns its own HOME, identity and hooks directory and never inherits
// GIT_* state, so no hand-rolled `git init` or env scrub is needed here.
func initGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-gate-integration"})
	if _, err := repo.Commit(t.Context(), "c1", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return repo.Dir
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
	return createBead(t, ws, title, "--defer", "2126-01-01")
}

// createBead creates a bead titled title with the extra bd create flags and
// returns its id.
func createBead(t *testing.T, ws, title string, extra ...string) string {
	t.Helper()
	out := runTool(t, ws, "bd", append([]string{"create", title}, append(extra, "--json")...)...)
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
