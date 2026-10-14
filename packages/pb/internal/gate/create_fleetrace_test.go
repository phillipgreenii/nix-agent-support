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
//
// The Attach cases at the bottom run the same real bd through gate.Attach
// (`pb gate attach-verified-child`): the verification child must be born with
// the implementation bead's scope labels, a description and acceptance
// criteria (bead pg2-m7itm) and still be held out of `bd ready` by its gate.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
	fleetRaceBeadHeldUntilGateResolves(t)
}

// fleetRaceBeadHeldUntilGateResolves is the body of the test above, shared with
// the foreign-git-environment guard (hermetic_env_test.go).
func fleetRaceBeadHeldUntilGateResolves(t *testing.T) {
	t.Helper()
	requireBinaries(t, "bd", "git")
	isolateBeadsEnv(t)

	ws := initGitRepoWithCommit(t)
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

// shownBead is what the Attach cases read back from `bd show --json`. It is
// parsed here, not through bd.Client.ShowBead, so the code under test is not
// its own witness.
type shownBead struct {
	ID                 string   `json:"id"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	Labels             []string `json:"labels"`
}

func showBeadJSON(t *testing.T, ws, id string) shownBead {
	t.Helper()
	out := runTool(t, ws, "bd", "show", id, "--json")
	var env struct {
		Data []shownBead `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || len(env.Data) != 1 {
		t.Fatalf("parse bd show %s: %v\n%s", id, err, out)
	}
	return env.Data[0]
}

// attachFixture stands up an isolated bd database in a git repo (one commit,
// the gated change) and an implementation bead carrying a repo label, a project
// label and several workflow-state labels. The returned func builds Attach's
// deps with a pn faked to describe that repo; Attach asks pn once itself and
// once per gate, so each call scripts exactly that many answers.
func attachFixture(t *testing.T) (ws, impl string, newDeps func(gates int) gate.CreateDeps) {
	t.Helper()
	requireBinaries(t, "bd", "git")
	isolateBeadsEnv(t)
	ws = initGitRepoWithCommit(t)
	runTool(t, ws, "bd", "init", "--prefix", "av")
	impl = createBead(t, ws, "implement the thing",
		"--labels", "agent-support,pb,human,escalated,worker-ready,has-acceptance-criteria")

	info := fmt.Sprintf(
		`{"wsid":"home","root":%q,"terminal":"m","repos":[{"name":"repo-a","path":%q,"applied_ref":"main","dirty":false}]}`,
		ws, ws,
	)
	real := hermeticCLIRunner{}
	return ws, impl, func(gates int) gate.CreateDeps {
		fakePN := run.NewFakeRunner()
		for range 1 + gates {
			fakePN.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
		}
		return gate.CreateDeps{PN: pn.Client{R: fakePN}, BD: bd.Client{R: real}, PatchID: patchid.Client{R: real}, R: real}
	}
}

// The fix for the verification child that pb used to create with only a title:
// after Attach succeeds the child carries the implementation bead's repo and
// project labels (and none of its workflow-state ones), a description and
// acceptance criteria, and is STILL held out of `bd ready` by its gate until
// the gate resolves — the deferred-first sequence is unchanged.
func TestFleetRace_attachedChildCarriesLabelsAndContentAndStaysGated(t *testing.T) {
	ws, impl, newDeps := attachFixture(t)

	attach := func(t *testing.T, title, description, acceptance string) (string, gate.AttachResult) {
		t.Helper()
		out, err := gate.Attach(context.Background(), newDeps(1), gate.AttachParams{
			WorkspaceDir: ws, ImplID: impl, Title: title,
			Gates: []gate.GateSpec{{Repo: "repo-a", Commit: "HEAD"}}, Actor: "t-drain",
			Reason: "post-deploy verify for " + impl, Description: description, Acceptance: acceptance,
		})
		if err != nil {
			t.Fatalf("gate.Attach: %v", err)
		}
		if len(out.Gates) != 1 || out.CommentFailed {
			t.Fatalf("out = %+v, want one gate and a recorded comment", out)
		}
		return out.ChildID, out
	}
	wantLabels := []string{"agent-support", "has-acceptance-criteria", "pb"}

	t.Run("derived description and acceptance", func(t *testing.T) {
		child, out := attach(t, "verify the thing works after apply", "", "")
		got := showBeadJSON(t, ws, child)
		if slices.Sort(got.Labels); !slices.Equal(got.Labels, wantLabels) {
			t.Errorf("labels = %v, want %v (implementation bead had workflow-state labels too)", got.Labels, wantLabels)
		}
		if !strings.Contains(got.Description, impl) || strings.TrimSpace(got.AcceptanceCriteria) == "" {
			t.Errorf("description = %q, acceptance = %q; want a description naming %s and non-empty criteria", got.Description, got.AcceptanceCriteria, impl)
		}
		// Still gated: the label and criteria that make it claimable must not
		// make it workable before the gate resolves.
		if inReady(t, ws, child) {
			t.Fatalf("child %s must NOT be ready while gate %s is open", child, out.Gates[0].GateID)
		}
		// Control: resolving the gate makes the SAME child ready, so absence above
		// was the gate, not a broken child.
		runTool(t, ws, "bd", "gate", "resolve", out.Gates[0].GateID, "--reason", "applied")
		if !inReady(t, ws, child) {
			t.Fatalf("child %s must be ready once its gate resolves", child)
		}
	})

	t.Run("given description and acceptance are stored as given", func(t *testing.T) {
		const description = "## Checks\n\n- run `tldr wsplan` and compare it with a sibling page"
		const acceptance = "- tldr wsplan renders the new page\n- the sibling page is unchanged"
		child, out := attach(t, "verify the second thing works after apply", description, acceptance)
		got := showBeadJSON(t, ws, child)
		if strings.TrimSpace(got.Description) != description || strings.TrimSpace(got.AcceptanceCriteria) != acceptance {
			t.Errorf("description = %q, acceptance = %q; want the given text", got.Description, got.AcceptanceCriteria)
		}
		if slices.Sort(got.Labels); !slices.Equal(got.Labels, wantLabels) {
			t.Errorf("labels = %v, want %v", got.Labels, wantLabels)
		}
		if inReady(t, ws, child) {
			t.Fatalf("child %s must NOT be ready while gate %s is open", child, out.Gates[0].GateID)
		}
	})
}
