package gate

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// stubDiscoverWS returns a single DB at /ws (bypasses the FS-walking discover).
func stubDiscoverWS(_ []string, _ string) ([]discover.DB, error) {
	return []discover.DB{{Dir: "/ws", Identity: "id-ws"}}, nil
}

const createInfoJSON = `{"wsid":"home","root":"/ws","terminal":"m",
	"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"base1","dirty":false}]}`

func TestCreate_singleCommitDefaultHEAD(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	// resolveBeadDB: HasBead at /ws
	f.AddResponse("bd", []string{"-C", "/ws", "show", "b-1", "--json"}, run.Result{Stdout: "{}"}, nil)
	// git show HEAD | patch-id --stable
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "HEAD"}, run.Result{Stdout: "diff..."}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 deadsha\n"}, nil)
	// bd gate create (co-located at /ws)
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
		"--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json",
	},
		run.Result{Stdout: `{"data":{"id":"g-1"}}`}, nil)
	// bd update --set-metadata applied_baseline=base1
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-1", "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)

	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
	out, err := Create(context.Background(), d, CreateParams{
		WorkspaceDir: "/ws", BeadID: "b-1", Repo: "repo-a", Reason: "pn:applied gate",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(out.Gates) != 1 {
		t.Fatalf("gates = %d", len(out.Gates))
	}
	g := out.Gates[0]
	if g.GateID != "g-1" || g.AwaitID != "home:repo-a:abc123" || g.PatchID != "abc123" || g.AppliedBaseline != "base1" {
		t.Errorf("gate = %+v", g)
	}
}

func TestCreate_unknownRepoErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
	_, err := Create(context.Background(), d, CreateParams{WorkspaceDir: "/ws", BeadID: "b-1", Repo: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
}

func TestCreate_multiCommitOneGatePerCommit(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "b-1", "--json"}, run.Result{Stdout: "{}"}, nil)
	// rev-list yields two commits
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "rev-list", "--no-merges", "--reverse", "base1..HEAD"},
		run.Result{Stdout: "sha1\nsha2\n"}, nil)
	// patch-id of each
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "sha1"}, run.Result{Stdout: "d1"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "pid1 sha1\n"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "sha2"}, run.Result{Stdout: "d2"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "pid2 sha2\n"}, nil)
	// one gate per commit, both blocking b-1
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
		"--await-id", "home:repo-a:pid1", "--reason", "r", "--json",
	}, run.Result{Stdout: `{"data":{"id":"g-1"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-1", "--set-metadata", "applied_baseline=base1"}, run.Result{}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
		"--await-id", "home:repo-a:pid2", "--reason", "r", "--json",
	}, run.Result{Stdout: `{"data":{"id":"g-2"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-2", "--set-metadata", "applied_baseline=base1"}, run.Result{}, nil)

	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
	out, err := Create(context.Background(), d, CreateParams{
		WorkspaceDir: "/ws", BeadID: "b-1", Repo: "repo-a", Commits: "base1..HEAD", Reason: "r",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(out.Gates) != 2 {
		t.Fatalf("gates = %d, want 2 (one per commit, both blocking b-1)", len(out.Gates))
	}
	if out.Gates[0].PatchID != "pid1" || out.Gates[1].PatchID != "pid2" {
		t.Errorf("patch-ids = %q,%q", out.Gates[0].PatchID, out.Gates[1].PatchID)
	}
}

// TestCreate_unpushedCommitInPinnedRepoIsCreatedSilently pins the DELIBERATE
// non-change on the create side (bead pg2-ft60a). The whole remedy is on the
// RESOLUTION side; gating a commit that is not yet pushed and relocked is normal
// and correct — the gate exists precisely to hold the follow-up until it ships.
//
// So even when `pn workspace info` says this repo is a terminal flake input whose
// locked rev is behind, `pb gate create` MUST behave exactly as it always did: the
// gate is created with the same await-id and baseline, no refusal, no warning, no
// extra flag, and — asserted here — no lock inspection at all. A creation-time
// refusal or warning would be non-conforming, and this test fails if one is added:
// FakeRunner errors on any unscripted call, and the call list is checked for a
// merge-base probe.
func TestCreate_unpushedCommitInPinnedRepoIsCreatedSilently(t *testing.T) {
	f := run.NewFakeRunner()
	// repo-a is a flake input of the terminal, applied at local HEAD "base1" but
	// built from locked rev "locked1" — i.e. the commit being gated is not in the
	// applied system. Create must not care.
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: `{"wsid":"home","root":"/ws","terminal":"m","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"base1","dirty":false,
		 "applied_state_schema":2,"terminal_input":true,"locked_rev":"locked1"}]}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "b-1", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "HEAD"}, run.Result{Stdout: "diff..."}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 unpushedsha\n"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
		"--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json",
	}, run.Result{Stdout: `{"data":{"id":"g-1"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-1", "--set-metadata", "applied_baseline=base1"}, run.Result{}, nil)

	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
	out, err := Create(context.Background(), d, CreateParams{
		WorkspaceDir: "/ws", BeadID: "b-1", Repo: "repo-a", Reason: "pn:applied gate",
	})
	if err != nil {
		t.Fatalf("Create must not refuse an unpushed commit: %v", err)
	}
	if len(out.Gates) != 1 {
		t.Fatalf("gates = %+v, want exactly one", out.Gates)
	}
	g := out.Gates[0]
	if g.GateID != "g-1" || g.AwaitID != "home:repo-a:abc123" || g.PatchID != "abc123" || g.AppliedBaseline != "base1" {
		t.Errorf("gate = %+v; create's output contract must be unchanged", g)
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) > 3 && c.Args[2] == "merge-base" {
			t.Fatalf("create must not inspect the lock/ancestry: %s %v", c.Name, c.Args)
		}
	}
}

func TestCreate_beadNotInAnyDBErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	// HasBead unscripted → FakeRunner errors → HasBead false → resolveBeadDB fails
	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
	_, err := Create(context.Background(), d, CreateParams{WorkspaceDir: "/ws", BeadID: "ghost", Repo: "repo-a"})
	if err == nil {
		t.Fatal("expected error when bead not found in any DB")
	}
}
