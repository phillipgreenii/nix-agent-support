package goldencorpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The command-text tokens MarkerTree.Expand replaces.
const (
	TokenWorkspaceSet  = "{{WORKSPACE_SET}}"
	TokenWorkspaceBare = "{{WORKSPACE_BARE}}"
)

// MarkerTree is the throwaway directory tree a corpus row naming
// WorkspaceMarkers is evaluated in: Root carries the marker files; Set is a
// workforest-set shaped directory two levels below it (so the marker is found
// ABOVE it, like a real pn workforest set); Bare is a sibling directory with no
// marker anywhere above it. CWD/ProjectRoot is whichever of Set/Bare the row's
// WorkspaceCWD names. The command text may name the directories with the
// tokens TokenWorkspaceSet/TokenWorkspaceBare (see Expand), which is how a row
// exercises a `cd <dir> && ...` form against the cd-tracked working directory
// (bead pg2-xj975): a real directory is needed because TrustedCheckoutExec
// looks at the filesystem. For a row without markers Set and Bare are empty, CWD
// and ProjectRoot are the row's synthetic paths, and Expand is a no-op.
type MarkerTree struct {
	CWD, ProjectRoot string
	Set, Bare        string
	Cleanup          func()
}

// Expand replaces the directory tokens in s (a no-op for a row without
// markers, which has no tree).
func (t MarkerTree) Expand(s string) string {
	if t.Set == "" {
		return s
	}
	return strings.NewReplacer(TokenWorkspaceSet, t.Set, TokenWorkspaceBare, t.Bare).Replace(s)
}

// MaterializeTree returns the working directory and project root a corpus row
// is evaluated in, for every consumer that GRADES or TIMES the row's verdict
// (evaluate --corpus, legacyextract's comparison, the Evaluate benchmark).
//
// A row with no WorkspaceMarkers keeps its synthetic fixture paths. A row
// naming markers gets a real throwaway tree instead (see MarkerTree); its
// request cwd is the set directory, or the marker-less sibling when
// WorkspaceCWD is "bare". The tree is created under literal /tmp (not $TMPDIR)
// so it lands outside every zone the path classifier recognises, like the
// effectpolicy golden fixtures. Cleanup removes it and is always safe to call.
// A consumer that replays the row's command MUST pass it through Expand.
func (fx PathStateFixture) MaterializeTree() (MarkerTree, error) {
	none := MarkerTree{CWD: fx.CWD, ProjectRoot: fx.ProjectRoot, Cleanup: func() {}}
	if len(fx.WorkspaceMarkers) == 0 {
		return none, nil
	}
	for _, m := range fx.WorkspaceMarkers {
		if m == "" || m != filepath.Base(m) || m == "." || m == ".." {
			return none, fmt.Errorf("workspace marker %q must be a bare file name", m)
		}
	}
	if fx.WorkspaceCWD != "" && fx.WorkspaceCWD != "set" && fx.WorkspaceCWD != "bare" {
		return none, fmt.Errorf("workspace_cwd %q must be \"set\" or \"bare\"", fx.WorkspaceCWD)
	}
	base, err := os.MkdirTemp("/tmp", "ceta-corpus-ws-")
	if err != nil {
		return none, fmt.Errorf("creating the marker workspace: %w", err)
	}
	t := MarkerTree{Cleanup: func() { _ = os.RemoveAll(base) }}
	root := filepath.Join(base, "ws")
	t.Set = filepath.Join(root, ".workforests", "set")
	t.Bare = filepath.Join(base, "bare")
	for _, dir := range []string{t.Set, t.Bare} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Cleanup()
			return none, fmt.Errorf("creating the marker workspace: %w", err)
		}
	}
	for _, m := range fx.WorkspaceMarkers {
		if err := os.WriteFile(filepath.Join(root, m), nil, 0o644); err != nil {
			t.Cleanup()
			return none, fmt.Errorf("writing marker %q: %w", m, err)
		}
	}
	t.CWD = t.Set
	if fx.WorkspaceCWD == "bare" {
		t.CWD = t.Bare
	}
	t.ProjectRoot = t.CWD
	return t, nil
}

// Materialize is MaterializeTree's (cwd, projectRoot, cleanup) view, for a
// caller that never replays a command containing the directory tokens.
func (fx PathStateFixture) Materialize() (cwd, projectRoot string, cleanup func(), err error) {
	t, err := fx.MaterializeTree()
	return t.CWD, t.ProjectRoot, t.Cleanup, err
}
