package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedSharedLayout is the module's shared-surface allowlist: only
// pkg/schema, pkg/provider (including its per-capability subpackages, e.g.
// pkg/provider/pr — the small-per-capability-interface convention named by
// design §3), and pkg/scriptout (including its own schemas/conformance
// subpackages, bead pg2-7vgn5 — see below) may be shared across backend
// boundaries — every backend's own code must live in main or under its own
// cmd/<binary>/internal/.
var allowedSharedLayout = map[string]bool{
	"pkg/schema":    true,
	"pkg/provider":  true,
	"pkg/scriptout": true,
}

// Both prefixes name a package whose OWN per-something subpackages are
// shared surface for the identical reason: pkg/provider/pr etc. are small,
// capability-scoped provider interfaces a Tier-2 backend implements;
// pkg/scriptout/schemas and pkg/scriptout/conformance (bead pg2-7vgn5) are
// the wire protocol's own schemas/goldens/conformance suite — a new backend
// author needs to import them (conformance.Run and friends) to validate
// their own implementation against the canonical wire shape, exactly the
// gap the design doc's Appendix A "Wire protocol and testing" flagged.
var allowedSharedLayoutPrefixes = []string{"pkg/provider/", "pkg/scriptout/"}

// evaluateLayoutConvention walks moduleRoot and returns one violation string
// per .go file whose package sits outside both the shared surface above and
// a backend's own cmd/<binary>/ isolation boundary.
//
// A path under cmd/ is fine ONLY when it is (a) directly in cmd/<binary>/
// itself — that binary's own package main, never importable by another
// backend regardless of what it exports, since nothing outside this module
// ever imports a package main; or (b) nested under a cmd/<binary>/internal/
// tree at any depth — Go's internal/ visibility rule is compiler-enforced
// per import-path text [design: §5.2's "cross-backend isolation is
// compiler-enforced via independent internal/ trees"].
//
// Any OTHER non-internal subdirectory under a backend's own cmd/<binary>/ —
// e.g. cmd/pg-connector-pr-github/util/ — is a real gap: Go's internal/
// visibility rule is the ONLY mechanism this module relies on to stop one
// backend importing another's private code, and a plain (non-internal,
// non-main) package underneath a backend's cmd/<binary>/ carries none of
// that protection, so it is importable by any sibling backend that chooses
// to. That is the exact gap this function closes over the original (looser)
// "any path under cmd/ is fine" rule.
func evaluateLayoutConvention(moduleRoot string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
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
		rel = filepath.ToSlash(rel)
		relDir := filepath.ToSlash(filepath.Dir(rel))
		if relDir == "." {
			// Module-root package files (if any) are fine.
			return nil
		}
		if allowedSharedLayout[relDir] {
			return nil
		}
		for _, prefix := range allowedSharedLayoutPrefixes {
			if strings.HasPrefix(relDir, prefix) {
				return nil
			}
		}
		if strings.HasPrefix(relDir, "cmd/") {
			segments := strings.Split(relDir, "/")
			// segments[0] == "cmd", segments[1] == the binary's own
			// directory name (e.g. "pg-connector-pr-github").
			if len(segments) == 2 {
				// Directly in cmd/<binary>/ — that binary's own package
				// main. Fine regardless of depth-0 exports.
				return nil
			}
			for _, seg := range segments[2:] {
				if seg == "internal" {
					// Nested under this binary's own internal/ tree —
					// compiler-enforced isolation covers it.
					return nil
				}
			}
			violations = append(violations, fmt.Sprintf(
				"%s: package %q is a non-internal package under a backend's cmd/ tree and is therefore importable by sibling backends — move it under this binary's own cmd/<binary>/internal/",
				rel, relDir,
			))
			return nil
		}
		violations = append(violations, fmt.Sprintf(
			"%s: package %q is outside the shared surface (pkg/schema, pkg/provider, pkg/scriptout) and outside cmd/<binary>/ — move it under a backend's own cmd/<binary>/internal/ or into the shared surface",
			rel, relDir,
		))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// TestBackendLayoutConvention is the cheap CI/convention check named by the
// Tier-1 core packet: nothing stops a backend from exporting a stray
// non-internal package another backend could import, so this backstops the
// compiler-enforced internal/ visibility boundary.
func TestBackendLayoutConvention(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, err := evaluateLayoutConvention(moduleRoot)
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestBackendLayoutConvention_RejectsNonInternalCmdPackage is a
// test-of-a-test: it proves evaluateLayoutConvention actually rejects the
// exact gap this bead exists to close (a non-internal exported package
// under a backend's own cmd/<binary>/ tree, importable by sibling
// backends) rather than merely passing against the current, already
// compliant, real tree. It builds a synthetic module layout in a temp
// directory — never touching the real tree, so the violation it proves
// never lands in this repo — and asserts:
//
//   - a file directly in cmd/<binary>/ (package main) is allowed;
//   - a file under cmd/<binary>/internal/... is allowed;
//   - a file under cmd/<binary>/<non-internal>/... is REJECTED — this is
//     exactly the case the original (pre-fix) rule "allows any path under
//     cmd/" let through unchecked.
func TestBackendLayoutConvention_RejectsNonInternalCmdPackage(t *testing.T) {
	root := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// Allowed: the backend's own top-level main package.
	writeFile("cmd/pg-connector-fake-backend/main.go", "package main\n")
	// Allowed: nested under the backend's own internal/ tree.
	writeFile("cmd/pg-connector-fake-backend/internal/thing/thing.go", "package thing\n")
	// VIOLATION: a non-internal, non-top-level package under the backend's
	// own cmd/<binary>/ tree — importable by any sibling backend.
	writeFile("cmd/pg-connector-fake-backend/util/helper.go", "package util\n\n// Helper is exported and lives outside internal/, so a sibling\n// backend could import it directly.\nfunc Helper() {}\n")

	violations, err := evaluateLayoutConvention(root)
	if err != nil {
		t.Fatalf("evaluateLayoutConvention: %v", err)
	}

	const wantOffender = "cmd/pg-connector-fake-backend/util/helper.go"
	found := false
	for _, v := range violations {
		if strings.HasPrefix(v, wantOffender+":") {
			found = true
		}
		if strings.HasPrefix(v, "cmd/pg-connector-fake-backend/main.go:") ||
			strings.HasPrefix(v, "cmd/pg-connector-fake-backend/internal/thing/thing.go:") {
			t.Errorf("evaluateLayoutConvention flagged an allowed path: %s", v)
		}
	}
	if !found {
		t.Fatalf("evaluateLayoutConvention did not flag %s as a violation; got: %v", wantOffender, violations)
	}
}
