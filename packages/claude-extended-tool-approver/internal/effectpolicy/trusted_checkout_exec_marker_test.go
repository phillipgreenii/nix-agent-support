package effectpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// markerWorkspace builds a pn workspace root that is NOT a git or go
// checkout: it carries only the pn-workspace.toml marker, with a workforest
// set directory (`.workforests/feat`, itself carrying no marker of its own)
// beneath it, exactly the two places the repo-base skills run `pn`/`pnwf`
// from (`cd <SETDIR> && pn workspace build`; the non-git workspace root).
// bare is a sibling directory with no marker anywhere above it.
func markerWorkspace(t *testing.T) (root, set, bare string) {
	t.Helper()
	base := tempDirUnderRealTmp(t)
	root = filepath.Join(base, "ws")
	set = filepath.Join(root, ".workforests", "feat")
	bare = filepath.Join(base, "bare")
	for _, d := range []string{set, bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, set, bare
}

// TestTrustedCheckoutExecPolicy_PnWorkspaceMarker pins the operator's marker
// ruling (Phillip, 2026-10-12, bead pg2-qy5u8): a directory at or under a
// pn-workspace.toml is a trusted exec location, so TrustedCheckoutExec is
// Permitted there even with no git/go marker, and still Unknown anywhere
// without one (the negative case), including the parent of a marker root.
func TestTrustedCheckoutExecPolicy_PnWorkspaceMarker(t *testing.T) {
	root, set, bare := markerWorkspace(t)
	exec := cmddesc.Effect{Kind: cmddesc.EffectExec, Detail: "trusted checkout code", Source: "pn workspace build"}
	for _, tc := range []struct {
		name string
		cwd  string
		want FindingVerdict
	}{
		{"workspace root", root, Permitted},
		{"workforest set directory", set, Permitted},
		{"directory without the marker", bare, Unknown},
		{"parent of the marker root", filepath.Dir(root), Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, applies := (TrustedCheckoutExec{}).Judge(exec, PolicyContext{CWD: tc.cwd})
			if !applies || f.Verdict != tc.want {
				t.Errorf("applies=%v verdict=%s (%s), want %s", applies, f.Verdict, f.Reason, tc.want)
			}
		})
	}
}

// TestPnPnwfExecFactMarkerWorkspace is the end-to-end pin for pg2-qy5u8: the
// pn/pnwf verbs that run workspace code declare a real Exec fact, so they
// approve from a workforest set directory and the non-git workspace root
// (marker route), abstain from a directory without the marker, while the
// read-only introspection verbs stay inert and approve everywhere, and the
// verbs the schemas deliberately omit stay abstain even inside the marker.
func TestPnPnwfExecFactMarkerWorkspace(t *testing.T) {
	root, set, bare := markerWorkspace(t)
	reg := cmddesc.DefaultRegistry()
	eval := func(cwd, command string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: command, CWD: cwd, ProjectRoot: cwd}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}

	execForms := []string{
		"pn workspace build",
		"pn workspace flake-check",
		"pn workspace format",
		"pn workspace pre-commit-check",
		"pn workspace doctor",
		"pn workspace lock",
		"pn workspace clone",
		"pn workspace update",
		"pn workspace rebase main",
		"pn workspace apply",
		"pn workspace push",
		"pnwf sync-fetch --set",
		"pnwf update-relock --set",
		"pnwf cleanup my-branch",
		"go test ./...",
	}
	for _, c := range execForms {
		for _, where := range []struct{ name, cwd string }{{"root", root}, {"set", set}} {
			t.Run("approve/"+where.name+"/"+c, func(t *testing.T) {
				if resp := eval(where.cwd, c); resp.Decision != evalcontract.Approve {
					t.Fatalf("decision = %s, want approve (reason: %s)", resp.Decision, resp.Reason)
				}
			})
		}
		t.Run("abstain/no-marker/"+c, func(t *testing.T) {
			if resp := eval(bare, c); resp.Decision != evalcontract.Abstain {
				t.Fatalf("decision = %s, want abstain without the marker (reason: %s)", resp.Decision, resp.Reason)
			}
		})
	}

	// Not pinned here: a `cd <dir> && ...` prefix. TrustedCheckoutExec judges
	// the REQUEST cwd, not a cd-tracked one (pre-existing; `cd <elsewhere> &&
	// go test` behaves the same inside a git checkout), so the verdict for a
	// cd form follows the session's own cwd, which is why every form above is
	// evaluated with cwd set to the directory under test.

	// Read-only introspection declares no Exec fact: approved from anywhere,
	// exactly as before this change.
	for _, c := range []string{"pn workspace status", "pn workspace tree", "pn workspace info --json", "pnwf resolve --set", "pnwf land-plan my-branch"} {
		t.Run("inert/"+c, func(t *testing.T) {
			if resp := eval(bare, c); resp.Decision != evalcontract.Approve {
				t.Fatalf("decision = %s, want approve (reason: %s)", resp.Decision, resp.Reason)
			}
		})
	}

	// The marker never widens what the schemas deliberately leave unmodeled.
	for _, c := range []string{
		"pn workspace init",
		"pn workspace allow",
		"pn workspace upgrade",
		"pn workspace workforest remove feat",
		"pn workspace nix -- build .#x",
		"pn workspace push --no-verify",
		"pnwf cleanup my-branch --force-dirty-worktree-removal",
		"pnwf cleanup my-branch --force-unlanded-branch-removal",
		"go run ./cmd/tool",
	} {
		t.Run("still-abstain/"+c, func(t *testing.T) {
			if resp := eval(set, c); resp.Decision == evalcontract.Approve {
				t.Fatalf("decision = approve inside the marker, want not-approve (reason: %s)", resp.Reason)
			}
		})
	}
}
