package effectpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// realHostFixture builds a throwaway project root and HOME exactly like
// golden_test.go's own fixture(), EXCEPT both live under the ambient, real
// $HOME's own .cache directory rather than under Go's t.TempDir() — which
// defaults to $TMPDIR, itself defaulting to literal "/tmp" when unset (this
// machine, and any plain Linux box with $TMPDIR unset). patheval's
// classify() hardcodes a "/tmp/**" -> PathReadWrite zone (evaluator.go's own
// comment: "deliberately NOT keyed on $TMPDIR ... /tmp itself is the tool's
// own well-known agent-scratch convention"), so a HOME placed under literal
// /tmp lands inside that zone BEFORE any more specific rule (the ~/.claude
// and ~/go/pkg zones, or — the gap this slice closes — a would-be ~/.ssh
// rule) is ever consulted. internal/patheval/main_test.go documents and
// works around the IDENTICAL collision for patheval's own test suite
// (redirectTempDirAwayFromLiteralTmp); this is that same fix, scoped to one
// fixture rather than a package-wide TestMain, since golden_test.go's own
// fixture() DELIBERATELY keeps the temp-root placement for the
// deletable-class coverage it exists to prove — see its own doc comment —
// which is not a safe stand-in for a real host's ~/.ssh/~/.aws zone, the
// exact question this file exists to answer (slice 3ab, tc-lc8f item 4h;
// tc-vn5z item 5's step-1 check-before-fix).
//
// It never reads or writes anything under the REAL ~/.ssh, ~/.aws or any
// other real dotfile — only a fresh scratch project root and HOME are
// created under a NEW throwaway subdirectory (removed via t.Cleanup), and
// every command evaluated against them is a pure string handed to Evaluate;
// nothing is ever actually executed.
func realHostFixture(t *testing.T) (root, home string) {
	t.Helper()
	ambient, err := os.UserHomeDir()
	if err != nil || ambient == "" {
		t.Skipf("no ambient $HOME to place a non-/tmp scratch root/HOME under: %v", err)
	}
	scratchParent := filepath.Join(ambient, ".cache", "ceta-effectpolicy-realhost-test")
	if err := os.MkdirAll(scratchParent, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", scratchParent, err)
	}
	scratch, err := os.MkdirTemp(scratchParent, "")
	if err != nil {
		t.Fatalf("create non-/tmp scratch dir under %s: %v", scratchParent, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })

	root = filepath.Join(scratch, "root")
	home = filepath.Join(scratch, "home")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "XDG_DATA_HOME"} {
		t.Setenv(v, "")
	}
	real := func(p string) string {
		r, evalErr := filepath.EvalSymlinks(p)
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		return r
	}
	realRoot, realHome := real(root), real(home)

	// tc-fpbpp: this helper's whole premise is a HOME that is NOT itself
	// shadowed by one of patheval's own hardcoded zones (see the doc comment
	// above — that is the literal reason it exists instead of reusing
	// golden_test.go's fixture()). Inside a `nix build` sandbox, though, the
	// AMBIENT $HOME this helper reads via os.UserHomeDir() (before the
	// t.Setenv("HOME", ...) above) is itself set by mkGoTest's buildPhase to
	// $TMPDIR, which on this host is NIX_BUILD_TOP under
	// /nix/var/nix/builds/<id> (internal/engine/engine.go's redirection
	// check documents the identical HOME=$TMPDIR buildPhase behavior, and
	// internal/patheval/escape_zone_ladder_test.go's pg2-lw19e writeup
	// root-causes the same $TMPDIR-under-/nix mechanism for a different
	// fixture). Every subdirectory built under that ambient HOME — including
	// this scratch dir — therefore ALSO resolves under /nix, and ADR 0068's
	// osNixKind (a Roots-based, unconditional prefix match, unlike the
	// Home-anchored zones this helper exists to exercise) classifies it
	// Forbidden regardless of the synthetic HOME this helper points
	// pathspec at. Unlike the tmp-root collision internal/patheval/
	// main_test.go's TestMain fixes (which safely relocates to a fresh
	// directory under the ambient HOME), there is no available escape here:
	// the ambient HOME itself is the thing that is nix-shadowed in this
	// exact environment, and literal /tmp is not a substitute (it is
	// osTmpKind/tempKind's OWN full-ReadWrite zone, which would silently
	// mask the real-host HOME-zone behavior this fixture exists to prove —
	// see TestPathAccessPolicy_GOMODCACHE_RealHost's own doc comment for why
	// golden_test.go's temp-shadowed fixture cannot substitute for this one
	// either). Skip rather than fail, mirroring escape_zone_ladder_test.go's
	// own established precedent for this identical class of nix-sandbox
	// artifact — a genuine classify()/pathspec regression on a normal
	// machine (where ambient HOME is never under /nix) still hard-fails.
	if strings.HasPrefix(realHome, "/nix/") || strings.HasPrefix(realRoot, "/nix/") {
		t.Skipf("realHostFixture: ambient $HOME resolves under /nix (%s) — this build's HOME=$TMPDIR=NIX_BUILD_TOP, "+
			"so no non-shadowed real-host HOME is available in this sandbox; nix-sandbox artifact, not a defect (see comment above)", realHome)
	}
	return realRoot, realHome
}

// TestNoWriteToSecretPath_RealHost is slice 3ab's (tc-lc8f item 4h; tc-vn5z
// item 5) permanent proof that the write-side secret-path Forbid does not
// depend on golden_test.go's own temp-root-shadowed fixture: the same five
// commands, evaluated against a HOME that is NOT itself inside patheval's
// hardcoded /tmp zone, reach the same verdicts golden_test.go's (shadowed)
// fixture reaches, for the same structural reason — WellKnownSecret's
// unconditional Forbid (NoWriteToSecretPath for create/modify/truncate,
// DeleteAccess's own secretRead check for delete) never consults patheval's
// zone at all.
//
// STEP 1 FINDING (recorded verbatim from the investigation that produced
// this slice, re-verified empirically by temporarily removing the
// remotePathGuard{NoWriteToSecretPath{}} entry from DefaultPolicies() and
// rerunning this exact test unchanged): BEFORE this slice's
// NoWriteToSecretPath existed, every write-class case below (cp/tee/echo/sed)
// ABSTAINED (Unknown) against this same real, non-shadowed HOME —
// patheval's zone table has no rule at all for ~/.ssh or ~/.aws (unlike
// ~/.claude, ~/go/pkg, which DO have explicit zone rules — evaluator.go's
// classify()), so an unzoned ~/.ssh/id_rsa or ~/.aws/credentials reached
// only PathUnknown, and NoWriteToReadOnlyPath's own "zone unknown" case is
// Unknown, not Forbidden. rm (AccessDelete) already Rejected before this
// slice and is unaffected by it: DeleteAccess's own ladder already
// consulted secretRead independently, before NoWriteToSecretPath existed.
// This confirmed the gap was REAL outside golden_test.go's fixture artifact
// (which would have hidden it: under that fixture's shadowed HOME, the same
// five commands would have reached Approve pre-fix — PathReadWrite from the
// /tmp/** rule, not merely Abstain — since nothing else in the policy set
// touches a write-class effect's secrecy).
func TestNoWriteToSecretPath_RealHost(t *testing.T) {
	root, _ := realHostFixture(t)
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name    string
		command string
		want    evalcontract.Decision
	}{
		{"cp_to_ssh_key", "cp README.md ~/.ssh/id_rsa", evalcontract.Reject},
		{"tee_ssh_config_stdin", "cat README.md | tee ~/.ssh/config", evalcontract.Reject},
		{"echo_redirect_aws_credentials", "echo x > ~/.aws/credentials", evalcontract.Reject},
		{"rm_ssh_key", "rm ~/.ssh/id_rsa", evalcontract.Reject},
		{"sed_i_ssh_config", "sed -i 's/a/b/' ~/.ssh/config", evalcontract.Reject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
			if resp.Decision != tc.want {
				t.Errorf("decision = %s, want %s (reason: %s)", resp.Decision, tc.want, resp.Reason)
			}
		})
	}
}
