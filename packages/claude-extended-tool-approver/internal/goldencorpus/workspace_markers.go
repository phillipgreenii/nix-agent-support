package goldencorpus

import (
	"fmt"
	"os"
	"path/filepath"
)

// Materialize returns the working directory and project root a corpus row is
// evaluated in, for every consumer that GRADES or TIMES the row's verdict
// (evaluate --corpus, legacyextract's comparison, the Evaluate benchmark).
//
// A row with no WorkspaceMarkers keeps its synthetic fixture paths. A row
// naming markers gets a real throwaway directory instead: the marker files
// are written (empty) in its root and the cwd is a workforest-set shaped
// directory two levels below, so the verdict depends on the marker being
// found ABOVE the cwd, exactly like a real pn workforest set. The directory
// is created under literal /tmp (not $TMPDIR) so it lands outside every zone
// the path classifier recognises, like the effectpolicy golden fixtures.
// cleanup removes it and is always safe to call.
func (fx PathStateFixture) Materialize() (cwd, projectRoot string, cleanup func(), err error) {
	if len(fx.WorkspaceMarkers) == 0 {
		return fx.CWD, fx.ProjectRoot, func() {}, nil
	}
	for _, m := range fx.WorkspaceMarkers {
		if m == "" || m != filepath.Base(m) || m == "." || m == ".." {
			return "", "", func() {}, fmt.Errorf("workspace marker %q must be a bare file name", m)
		}
	}
	root, err := os.MkdirTemp("/tmp", "ceta-corpus-ws-")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("creating the marker workspace: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(root) }
	cwd = filepath.Join(root, ".workforests", "set")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("creating the marker workspace: %w", err)
	}
	for _, m := range fx.WorkspaceMarkers {
		if err := os.WriteFile(filepath.Join(root, m), nil, 0o644); err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("writing marker %q: %w", m, err)
		}
	}
	return cwd, cwd, cleanup, nil
}
