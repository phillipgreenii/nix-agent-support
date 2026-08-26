//go:build integration

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
//
// requireBinaries/isolateBeadsEnv/initGitRepoWithCommit/runTool/
// createDeferredBead/inReady moved to gatetest_helpers_test.go (untagged) —
// lifecycle_smoke_test.go (tagged `smoke`) uses them too (bead pg2-h05lt).

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// hermeticCLIRunner wraps run.CLIRunner, filling in hermeticEnviron()
// (gatetest_helpers_test.go) whenever the caller leaves opts.Env nil.
// patchid.go's and gate/create.go's own git invocations both leave Env nil,
// so without this a `git` subprocess spawned by gate.Create below inherits
// GIT_DIR/GIT_INDEX_FILE/etc. from an enclosing git commit hook and gets
// redirected at the REAL repo instead of this test's own tmpdir workspace
// ws -- the same hermeticity leak patchid_test.go had before 98f8c95d.
type hermeticCLIRunner struct{}

func (hermeticCLIRunner) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if opts.Env == nil {
		opts.Env = hermeticEnviron()
	}
	return run.CLIRunner{}.Run(ctx, name, args, opts)
}

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
		ws, ws,
	)
	fakePN.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)

	real := hermeticCLIRunner{}
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
