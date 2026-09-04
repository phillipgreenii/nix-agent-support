package main

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackendLayoutConvention is the cheap CI/convention check named by the
// Tier-1 core packet: nothing stops a backend from exporting a stray
// non-internal package another backend could import, so this backstops the
// compiler-enforced internal/ visibility boundary. Only pkg/schema,
// pkg/provider (including its per-capability subpackages, e.g.
// pkg/provider/pr — the small-per-capability-interface convention named by
// design §3), and pkg/scriptout may be shared across backend boundaries —
// every backend's own code must live in main or under its own
// cmd/<binary>/internal/.
func TestBackendLayoutConvention(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	allowedShared := map[string]bool{
		"pkg/schema":    true,
		"pkg/provider":  true,
		"pkg/scriptout": true,
	}
	// pkg/provider's per-capability subpackages (pkg/provider/pr today;
	// pkg/provider/issue, pkg/provider/ci, pkg/provider/scm as they're
	// eventually built) are shared surface too — each is a small,
	// capability-scoped provider interface a Tier-2 backend for that
	// capability implements, exactly like pkg/provider/pr.Provider.
	allowedSharedPrefix := "pkg/provider/"

	err = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		if relDir == "." {
			// Module-root package files (if any) are fine.
			return nil
		}
		if allowedShared[relDir] || strings.HasPrefix(relDir, allowedSharedPrefix) {
			return nil
		}
		if strings.HasPrefix(relDir, "cmd/") {
			// Any file under a backend's own cmd/<binary>/... (including
			// cmd/<binary>/internal/...) is fine.
			return nil
		}
		t.Errorf("%s: package %q is outside the shared surface (pkg/schema, pkg/provider, pkg/scriptout) and outside cmd/<binary>/ — move it under a backend's own cmd/<binary>/internal/ or into the shared surface", rel, relDir)
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}
