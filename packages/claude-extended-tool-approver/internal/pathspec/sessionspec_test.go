package pathspec

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// sessionKinds wraps pe's SessionKind in the []Kind shape
// ResolveAccess/ClassifyWith take (tc-mkpaz.3's packet text: "you unit-test
// your new Kind(s) through it, exactly as P2 does").
func sessionKinds(pe *patheval.PathEvaluator) []Kind {
	return []Kind{SessionKind(pe)}
}

func TestSessionKind_AllowWrite_Permitted(t *testing.T) {
	pe := patheval.NewWithCWD("/ceta-session-project", "/ceta-session-project")
	root := "/ceta-session-test-allow-write"
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{AllowWrite: []string{root}})

	target := filepath.Join(root, "sub", "file.txt")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Write.Result != Permitted {
		t.Errorf("Write = %s (%q), want Permitted", pa.Write.Result, pa.Write.Reason)
	}
}

// TestSessionKind_AllowRead_IndependentGrant is the acceptance criterion
// distinguishing this packet's grant from today's override-only shape: a
// path under an allowRead root resolves Read: Permitted EVEN WITH NO
// corresponding denyRead entry — deliberately not configuring one here, so
// there is nothing for an override to cancel.
func TestSessionKind_AllowRead_IndependentGrant(t *testing.T) {
	pe := patheval.NewWithCWD("/ceta-session-project", "/ceta-session-project")
	root := "/ceta-session-test-allow-read"
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{AllowRead: []string{root}})

	target := filepath.Join(root, "sub", "file.txt")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Read.Result != Permitted {
		t.Errorf("Read = %s (%q), want Permitted (independent grant, no denyRead configured)", pa.Read.Result, pa.Read.Reason)
	}
	// allowRead alone never implies write.
	if pa.Write.Result == Permitted {
		t.Errorf("Write = Permitted, want not-Permitted (allowRead does not grant write)")
	}
}

func TestSessionKind_ExtraReadWriteRoots(t *testing.T) {
	rwRoot := "/ceta-session-test-extra-rw"
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", rwRoot)
	pe := patheval.New("/ceta-session-project")

	target := filepath.Join(rwRoot, "sub", "file.txt")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Read.Result != Permitted {
		t.Errorf("Read = %s, want Permitted", pa.Read.Result)
	}
	if pa.Write.Result != Permitted {
		t.Errorf("Write = %s, want Permitted", pa.Write.Result)
	}
}

func TestSessionKind_ExtraReadOnlyRoots(t *testing.T) {
	roRoot := "/ceta-session-test-extra-ro"
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", roRoot)
	pe := patheval.New("/ceta-session-project")

	target := filepath.Join(roRoot, "sub", "file.txt")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Read.Result != Permitted {
		t.Errorf("Read = %s, want Permitted", pa.Read.Result)
	}
	if pa.Write.Result == Permitted {
		t.Errorf("Write = Permitted, want not-Permitted (read-only root)")
	}
}

// TestSessionKind_ProjectRoot_GatedByFabricatedRootGuard: a fabricated
// project root that IS $HOME grants no zone — mirroring
// patheval.rootGrantsZone's existing effect (a too-broad/fabricated root at
// or above $HOME covers every dotfile the user owns).
func TestSessionKind_ProjectRoot_GatedByFabricatedRootGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pe := patheval.New(home) // fabricated root (no .git at home) == $HOME

	if pe.ProjectRootGrantsZone() {
		t.Fatalf("test setup invalid: ProjectRootGrantsZone() = true, want false for a fabricated root == $HOME")
	}

	target := filepath.Join(home, "dotfile")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Write.Result == Permitted {
		t.Errorf("Write = Permitted, want not-Permitted (fabricated project root covering $HOME must not grant a zone)")
	}
	if pa.Read.Result == Permitted {
		t.Errorf("Read = Permitted, want not-Permitted (fabricated project root covering $HOME must not grant a zone)")
	}
}

// TestSessionKind_ProjectRoot_NarrowFabricatedRootGrants is the positive
// control for the guard above: a fabricated root that does NOT cover $HOME
// still grants its zone, exactly as classify()'s <projectRoot>/** does
// today for an ordinary (non-broad) fabricated root.
func TestSessionKind_ProjectRoot_NarrowFabricatedRootGrants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	narrow := filepath.Join(home, "unrelated-narrow-root")
	pe := patheval.New(narrow) // fabricated, but narrower than $HOME

	if !pe.ProjectRootGrantsZone() {
		t.Fatalf("test setup invalid: ProjectRootGrantsZone() = false, want true for a narrow fabricated root")
	}

	target := filepath.Join(narrow, "file.go")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Write.Result != Permitted {
		t.Errorf("Write = %s, want Permitted", pa.Write.Result)
	}
	if pa.Read.Result != Permitted {
		t.Errorf("Read = %s, want Permitted", pa.Read.Result)
	}
}

// TestSessionKind_WorkspaceRoot_UngatedByFabricatedRootGuard is the
// asymmetry acceptance criterion: WORKSPACE_ROOT is set to the SAME
// too-broad root ($HOME) that TestSessionKind_ProjectRoot_
// GatedByFabricatedRootGuard proves gets NO grant as a project root — and
// yet, ported as WORKSPACE_ROOT, it still grants, because classify()'s
// WORKSPACE_ROOT/** branch carries no rootGrantsZone-style guard at all.
func TestSessionKind_WorkspaceRoot_UngatedByFabricatedRootGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WORKSPACE_ROOT", home)
	pe := patheval.New(home) // project root also fabricated == $HOME: gated (see above)

	if pe.ProjectRootGrantsZone() {
		t.Fatalf("test setup invalid: expected ProjectRootGrantsZone() = false for this scenario")
	}

	target := filepath.Join(home, "dotfile")
	pa := ResolveAccess(sessionKinds(pe), target)

	if pa.Write.Result != Permitted {
		t.Errorf("Write = %s (%q), want Permitted (WORKSPACE_ROOT grant must not be narrowed by the project-root guard)", pa.Write.Result, pa.Write.Reason)
	}
	if pa.Read.Result != Permitted {
		t.Errorf("Read = %s (%q), want Permitted (WORKSPACE_ROOT grant must not be narrowed by the project-root guard)", pa.Read.Result, pa.Read.Reason)
	}
}

// TestSessionKind_NilEvaluator: SessionKind(nil) must not panic and must
// simply grant nothing, mirroring ClassifyWith/NonSecretWithKinds' own
// nil-pe handling elsewhere in this package.
func TestSessionKind_NilEvaluator(t *testing.T) {
	pa := ResolveAccess(sessionKinds(nil), "/anything")
	if pa.Write.Result == Permitted || pa.Read.Result == Permitted {
		t.Errorf("PathAccess = %+v, want no Permitted facet for a nil evaluator", pa)
	}
}
