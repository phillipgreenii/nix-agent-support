package gate_test

// Fleet-race lifecycle test (design decision D1): the recommended usage creates
// the bead DEFERRED, then `pb gate create` attaches the gate, then the caller
// un-defers. This asserts the bead is NEVER in `bd ready` from create through
// gate-attach and un-defer, and only becomes ready once the gate resolves — so a
// fleet of agents can never grab a follow-up bead before its change is applied.
//
// Uses REAL bd (embedded Dolt in an isolated HOME/XDG temp) and REAL git; the pn
// workspace info is injected via a FakeRunner so no real `pn` is required. Skips
// when bd or git is absent (so it skips in the pure nix build sandbox and runs on
// a dev machine).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

func TestFleetRace_beadHeldUntilGateResolves(t *testing.T) {
	requireBinaries(t, "bd", "git")
	isolateBeadsEnv(t)

	ws := t.TempDir()
	initGitRepoWithCommit(t, ws)
	runTool(t, ws, "bd", "init", "--prefix", "fr")

	// Step 1: a deferred follow-up bead is hidden from `bd ready`.
	bead := createDeferredBead(t, ws, "verify code works")
	if inReady(t, ws, bead) {
		t.Fatalf("deferred bead %s must NOT be ready", bead)
	}

	// Step 2: pb gate create attaches a pn:applied gate (real bd + real git;
	// pn info faked to point at this temp workspace). applied_ref is a dummy
	// non-empty baseline so SetMetadata writes a real value.
	fakePN := run.NewFakeRunner()
	info := fmt.Sprintf(
		`{"wsid":"home","root":%q,"terminal":"m","repos":[{"name":"repo-a","path":%q,"applied_ref":"main","dirty":false}]}`,
		ws, ws)
	fakePN.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)

	real := run.CLIRunner{}
	deps := gate.CreateDeps{
		PN:      pn.Client{R: fakePN},
		BD:      bd.Client{R: real},
		PatchID: patchid.Client{R: real},
		R:       real,
		// Discover nil → real discover.DistinctDBs walks ws/.beads (embedded).
	}
	out, err := gate.Create(context.Background(), deps, gate.CreateParams{
		WorkspaceDir: ws, BeadID: bead, Repo: "repo-a", Reason: "pn:applied gate",
	})
	if err != nil {
		t.Fatalf("gate.Create: %v", err)
	}
	if len(out.Gates) != 1 {
		t.Fatalf("expected 1 gate, got %d: %+v", len(out.Gates), out.Gates)
	}
	gateID := out.Gates[0].GateID

	// Step 3: the gate holds the bead even though it is still deferred.
	if inReady(t, ws, bead) {
		t.Fatalf("bead %s must NOT be ready while gate %s is open", bead, gateID)
	}

	// Step 4: un-defer the bead — the gate still holds it.
	runTool(t, ws, "bd", "update", bead, "--defer", "")
	if inReady(t, ws, bead) {
		t.Fatalf("bead %s must NOT be ready: gate %s still holds it after un-defer", bead, gateID)
	}

	// Step 5: resolve the gate — now the bead is workable.
	runTool(t, ws, "bd", "gate", "resolve", gateID, "--reason", "applied")
	if !inReady(t, ws, bead) {
		t.Fatalf("bead %s must be ready after gate %s resolves", bead, gateID)
	}
}

// ---- helpers ----------------------------------------------------------------

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
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\nstderr: %s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func createDeferredBead(t *testing.T, ws, title string) string {
	t.Helper()
	out := runTool(t, ws, "bd", "create", title, "--defer", "+100y", "--json")
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
