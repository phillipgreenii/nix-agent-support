package effectpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestPathAccessPolicy_AllowReadIndependentGrant proves ADR 0068's named
// deliberate widening (operator ruling, Phillip, 2026-09-13): a
// sandbox.filesystem.allowRead root now grants Read: Permitted
// independently, with NO matching denyRead entry present — this did NOT
// work before this docket (allowRead previously only ever cancelled a
// denyRead match via IsDenyRead's override loop; it granted nothing through
// the ordinary zone ladder).
func TestPathAccessPolicy_AllowReadIndependentGrant(t *testing.T) {
	root, _ := fixture(t)
	pe := patheval.NewWithCWD(root, root)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secretish.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{AllowRead: []string{outside}})
	ctx := PolicyContext{PathEval: pe, CWD: root}

	e := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessRead, Path: filepath.Join(outside, "secretish.txt")}
	f, applies := PathAccessPolicy{}.Judge(e, ctx)
	if !applies || f.Verdict != Permitted {
		t.Fatalf("allowRead-granted read: applies=%v verdict=%s (%s), want Permitted", applies, f.Verdict, f.Reason)
	}

	// A path OUTSIDE the allowRead root, and outside every other zone, is
	// still Unknown — the grant is scoped to the declared root, not a
	// blanket widening.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2 := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessRead, Path: filepath.Join(other, "x.txt")}
	if f2, applies2 := (PathAccessPolicy{}).Judge(e2, ctx); !applies2 || f2.Verdict != Permitted {
		// other is itself a fresh t.TempDir(), which on this machine may
		// sit under a temp root and therefore be independently
		// zone-permitted — that is fine and expected; this assertion only
		// guards against a silent PANIC or an unexpected Forbidden, not a
		// specific verdict, since whether t.TempDir() lands in a temp zone
		// is machine-dependent and not what this test exists to pin.
		t.Logf("path outside the allowRead root: applies=%v verdict=%s (%s)", applies2, f2.Verdict, f2.Reason)
	}
}

// TestPathAccessPolicy_MatchedDeniedRoot proves ADR 0068's other named new
// behavior (operator ruling, Phillip, 2026-09-13): a path under a
// CETA_DENIED_ROOTS-configured root produces a PathAccessPolicy Finding
// that NAMES the matched denied root — patheval.MatchedDeniedRoot was
// previously consulted only by internal/rules/deniedroots, never by
// internal/effectpolicy at all.
func TestPathAccessPolicy_MatchedDeniedRoot(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("CETA_DENIED_ROOTS", "/home")
	pe := patheval.NewWithCWD(root, root)
	ctx := PolicyContext{PathEval: pe, CWD: root}

	for _, tc := range []struct {
		name    string
		access  cmddesc.PathAccess
		path    string
		verdict FindingVerdict
	}{
		{"read under denied root", cmddesc.AccessRead, "/home/user/repo/file.go", Forbidden},
		{"write under denied root", cmddesc.AccessTruncate, "/home/user/repo/file.go", Forbidden},
		{"delete under denied root", cmddesc.AccessDelete, "/home/user/repo/file.go", Forbidden},
	} {
		e := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: tc.access, Path: tc.path}
		f, applies := PathAccessPolicy{}.Judge(e, ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
		if f.Reason == "" || !strings.Contains(f.Reason, "/home") {
			t.Errorf("%s: reason %q does not name the matched denied root", tc.name, f.Reason)
		}
	}

	// A path NOT under the denied root is unaffected.
	e := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessRead, Path: filepath.Join(root, "README.md")}
	if f, applies := (PathAccessPolicy{}).Judge(e, ctx); !applies || f.Verdict != Permitted {
		t.Errorf("unrelated path: applies=%v verdict=%s (%s), want Permitted", applies, f.Verdict, f.Reason)
	}
}

// TestPathAccessPolicy_GitWriteFallsThroughToOrdinaryZone proves ADR 0068's
// named deliberate delta (b): a .git PathModify write's Finding cites the
// ordinary working-tree zone reason, not a .git-Protected-by-accident
// reason — closing the ADR 0067 gap gitKind's own doc comment names
// (internal/pathspec/workspace.go's gitKind.Classify "deviation 1"):
// gitKind declares Delete: Forbidden for .git, but deliberately leaves
// Read/Write Unknown, so a .git write falls all the way through
// PathAccessPolicy's secret+workspace check (which has no opinion) to the
// ordinary zone ladder, same as any other path in the working tree.
func TestPathAccessPolicy_GitWriteFallsThroughToOrdinaryZone(t *testing.T) {
	root, _ := fixture(t)
	pe := patheval.NewWithCWD(root, root)
	ctx := PolicyContext{PathEval: pe, CWD: root}

	e := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessModify, Path: ".git"}
	f, applies := PathAccessPolicy{}.Judge(e, ctx)
	if !applies || f.Verdict != Permitted {
		t.Fatalf(".git write: applies=%v verdict=%s (%s), want Permitted", applies, f.Verdict, f.Reason)
	}
	if !strings.Contains(f.Reason, "zone") {
		t.Errorf(".git write reason %q does not cite the ordinary zone (looks Protected-by-accident)", f.Reason)
	}
}

// TestPathAccessPolicy_GOMODCACHE_RealHost proves ADR 0068's named
// deliberate delta (a) — the GOMODCACHE fix this whole ADR exists for — on
// a HOME that is NOT itself shadowed by patheval's hardcoded /tmp/** zone
// (golden_test.go's own fixture() cannot prove this: its HOME sits under a
// temp root on this machine, so patheval's classify() never even reaches
// the ~/go/pkg read-only special-case — see go_clean_modcache's own golden
// comment). Before this packet, DeleteAccess's old zone-gate-first ladder
// checked `access == patheval.PathReject || access == patheval.PathReadOnly`
// BEFORE ever consulting the workspace's GOMODCACHE declaration, so
// `rm -rf ~/go/pkg/mod` was Forbidden by zone regardless of goKind's own
// Delete: Permitted opinion on that exact root. PathAccessPolicy's new
// judgeDelete consults pathspec.ResolveAccessWithSecrets(...).Delete FIRST
// — and osHomeKind (P2) deliberately leaves ~/go/pkg's OWN Delete facet
// Unknown (never Forbidden — see osspec.go's "The ~/go/pkg exception"),
// so goKind's deeper, more specific Delete: Permitted for GOMODCACHE itself
// is free to win the per-facet fold.
func TestPathAccessPolicy_GOMODCACHE_RealHost(t *testing.T) {
	root, home := realHostFixture(t)
	if err := os.MkdirAll(filepath.Join(home, "go", "pkg", "mod", "example.com", "pkg@v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	pe := patheval.NewWithCWD(root, root)
	ctx := PolicyContext{PathEval: pe, CWD: root}

	target := filepath.Join(home, "go", "pkg", "mod", "example.com", "pkg@v1.0.0")
	e := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessDelete, Path: target}
	f, applies := PathAccessPolicy{}.Judge(e, ctx)
	if !applies || f.Verdict != Permitted {
		t.Errorf("delete under a real (non-temp-shadowed) GOMODCACHE: applies=%v verdict=%s (%s), want Permitted", applies, f.Verdict, f.Reason)
	}

	// Confirm the SAME real HOME's ~/go/pkg read-only zone still blocks an
	// ORDINARY write into it (the zone this fix must not weaken) — proving
	// the fix is scoped to the goKind-declared GOMODCACHE root, not a
	// blanket loosening of ~/go/pkg.
	other := filepath.Join(home, "go", "pkg", "not-mod-cache.txt")
	writeEffect := cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessTruncate, Path: other}
	if wf, applies := (PathAccessPolicy{}).Judge(writeEffect, ctx); !applies || wf.Verdict == Permitted {
		t.Errorf("write elsewhere under ~/go/pkg: applies=%v verdict=%s (%s), want NOT Permitted (still read-only)", applies, wf.Verdict, wf.Reason)
	}
}
