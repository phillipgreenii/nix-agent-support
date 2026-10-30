package main

import (
	"fmt"
	"go/build"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Each Tier-2 backend <name>.nix builds from a filtered lib.fileset.toSource
// allowlist, and mkGoApp defaults doCheck = false, so neither `go build` nor
// the unit tests (both run against the FULL module tree) can see an import
// that reaches a package directory the allowlist omits — only the real nix
// build can, under -mod=vendor ("cannot find module providing package ...").
// This test closes that gap hermetically: it walks each backend's non-test
// import closure inside the module and requires every Go file on it to be
// covered by the fileset parsed out of the matching <name>.nix. It runs at
// commit time (run-unit-tests) and in checks.pg-connector-go-tests.
//
// The nix parsing is deliberately tiny and FAILS LOUDLY on anything it does
// not understand (an unknown lib.fileset function, a token naming a path that
// does not exist, a missing fileset block), so a refactor of the filesets
// cannot silently turn this into a vacuous pass.

var (
	nixFilesetBlockEnd = "\n  };"
	nixPathToken       = regexp.MustCompile(`\./[A-Za-z0-9_./-]*`)
	nixFilesetFunc     = regexp.MustCompile(`lib\.fileset\.([A-Za-z]+)`)
	// difference <base> (lib.fileset.unions [ <excluded>... ]) — the only
	// shape the backend filesets use.
	nixDifference = regexp.MustCompile(
		`lib\.fileset\.difference\s+(\./[A-Za-z0-9_./-]+)\s*\(\s*lib\.fileset\.unions\s*\[([^\]]*)\]\s*\)`,
	)
	moduleClause = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)
)

// nixFileset is the parsed allowlist: module-root-relative paths, "." meaning
// the whole tree.
type nixFileset struct {
	include []string
	exclude []string
}

func under(rel, prefix string) bool {
	return prefix == "." || rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

func (f nixFileset) covers(rel string) bool {
	covered := false
	for _, inc := range f.include {
		if under(rel, inc) {
			covered = true
			break
		}
	}
	if !covered {
		return false
	}
	for _, ex := range f.exclude {
		if under(rel, ex) {
			return false
		}
	}
	return true
}

func nixTokens(s string) []string {
	var out []string
	for _, tok := range nixPathToken.FindAllString(s, -1) {
		out = append(out, path.Clean(strings.TrimPrefix(tok, "./")))
	}
	return out
}

// parseNixFileset extracts the `fileset = ...;` allowlist of a backend .nix.
// moduleRoot is used only to verify every named path exists.
func parseNixFileset(moduleRoot, nixSrc string) (nixFileset, error) {
	var lines []string
	for line := range strings.SplitSeq(nixSrc, "\n") {
		line, _, _ = strings.Cut(line, "#")
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")

	_, block, found := strings.Cut(text, "fileset =")
	if !found {
		return nixFileset{}, fmt.Errorf("no `fileset =` block found")
	}
	end := strings.Index(block, nixFilesetBlockEnd)
	if end < 0 {
		return nixFileset{}, fmt.Errorf("fileset block is not terminated by %q", strings.TrimSpace(nixFilesetBlockEnd))
	}
	block = block[:end]

	for _, m := range nixFilesetFunc.FindAllStringSubmatch(block, -1) {
		if m[1] != "unions" && m[1] != "difference" {
			return nixFileset{}, fmt.Errorf("unsupported lib.fileset.%s in fileset block; extend parseNixFileset", m[1])
		}
	}

	var fs nixFileset
	rest := nixDifference.ReplaceAllStringFunc(block, func(match string) string {
		sub := nixDifference.FindStringSubmatch(match)
		fs.include = append(fs.include, nixTokens(sub[1])...)
		fs.exclude = append(fs.exclude, nixTokens(sub[2])...)
		return ""
	})
	if strings.Contains(rest, "lib.fileset.difference") {
		return nixFileset{}, fmt.Errorf("lib.fileset.difference in an unsupported shape; extend parseNixFileset")
	}
	fs.include = append(fs.include, nixTokens(rest)...)

	if len(fs.include) == 0 {
		return nixFileset{}, fmt.Errorf("fileset block names no paths")
	}
	for _, p := range append(append([]string{}, fs.include...), fs.exclude...) {
		if _, err := os.Stat(filepath.Join(moduleRoot, filepath.FromSlash(p))); err != nil {
			return nixFileset{}, fmt.Errorf("fileset names %q which does not exist under the module root", p)
		}
	}
	return fs, nil
}

// backendImportClosure returns every module-root-relative non-test Go file
// reachable from cmd/<backend> through module-internal imports, evaluated for
// both GOOS values the packages build on (meta.platforms = platforms.all).
func backendImportClosure(moduleRoot, modulePath, backend string) ([]string, error) {
	seen := map[string]bool{}
	files := map[string]bool{}
	queue := []string{path.Join("cmd", backend)}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		for _, goos := range []string{"linux", "darwin"} {
			ctx := build.Default
			ctx.GOOS = goos
			pkg, err := ctx.ImportDir(filepath.Join(moduleRoot, filepath.FromSlash(dir)), 0)
			if err != nil {
				if _, ok := err.(*build.NoGoError); ok {
					continue
				}
				return nil, fmt.Errorf("%s (GOOS=%s): %w", dir, goos, err)
			}
			for _, f := range append(append([]string{}, pkg.GoFiles...), pkg.CgoFiles...) {
				files[path.Join(dir, f)] = true
			}
			for _, imp := range pkg.Imports {
				if rel, ok := strings.CutPrefix(imp, modulePath+"/"); ok {
					queue = append(queue, rel)
				}
			}
		}
	}
	out := make([]string, 0, len(files))
	for f := range files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// evaluateFilesetCoverage returns one violation string per Go file a backend
// builds from that its <name>.nix fileset does not cover.
func evaluateFilesetCoverage(moduleRoot string) ([]string, error) {
	goMod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return nil, err
	}
	m := moduleClause.FindSubmatch(goMod)
	if m == nil {
		return nil, fmt.Errorf("no module clause in go.mod")
	}
	modulePath := string(m[1])

	entries, err := os.ReadDir(filepath.Join(moduleRoot, "cmd"))
	if err != nil {
		return nil, err
	}
	var violations []string
	checked := 0
	for _, e := range entries {
		// cmd/pg-connector itself is built from default.nix with whole ./cmd
		// and ./pkg, so only the per-backend binaries are filtered.
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "pg-connector-") {
			continue
		}
		name := e.Name()
		nixSrc, err := os.ReadFile(filepath.Join(moduleRoot, name+".nix"))
		if err != nil {
			violations = append(violations, fmt.Sprintf("cmd/%s has no %s.nix beside go.mod", name, name))
			continue
		}
		fs, err := parseNixFileset(moduleRoot, string(nixSrc))
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s.nix: %v", name, err))
			continue
		}
		files, err := backendImportClosure(moduleRoot, modulePath, name)
		if err != nil {
			return nil, err
		}
		checked++
		for _, f := range files {
			if !fs.covers(f) {
				violations = append(violations, fmt.Sprintf(
					"%s.nix: fileset does not cover %s, which cmd/%s imports (nix build would fail under -mod=vendor)", name, f, name,
				))
			}
		}
	}
	if checked == 0 && len(violations) == 0 {
		return nil, fmt.Errorf("no cmd/pg-connector-* backends found; the test would pass vacuously")
	}
	return violations, nil
}

func TestBackendFilesetCoversImports(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, err := evaluateFilesetCoverage(moduleRoot)
	if err != nil {
		t.Fatalf("evaluate fileset coverage: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

const filesetFixtureNix = `{ lib, mkGoApp, ... }:
mkGoApp {
  # fileset = ./not/a/real/entry (comments are ignored)
  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
%s
      ./cmd/pg-connector-x
    ];
  };
}
`

func writeFilesetFixture(t *testing.T, nixEntries string) string {
	t.Helper()
	root := t.TempDir()
	writeCompositionFixture(t, root, "go.mod", "module example.com/m\n\ngo 1.26.0\n")
	writeCompositionFixture(t, root, "cmd/pg-connector-x/main.go",
		"package main\n\nimport _ \"example.com/m/pkg/a\"\n\nfunc main() {}\n")
	writeCompositionFixture(t, root, "pkg/a/a.go", "package a\n\nimport _ \"example.com/m/pkg/b\"\n")
	writeCompositionFixture(t, root, "pkg/b/b.go", "package b\n")
	writeCompositionFixture(t, root, "pkg/b/skip/skip.go", "package skip\n")
	writeCompositionFixture(t, root, "pg-connector-x.nix", fmt.Sprintf(filesetFixtureNix, nixEntries))
	return root
}

// TestBackendFilesetCoversImports_DetectsMissingTransitivePackage proves the
// check flags the exact miss it exists for: pkg/a is listed, but the pkg/b it
// imports (a TRANSITIVE import, not one the cmd names directly) is not.
func TestBackendFilesetCoversImports_DetectsMissingTransitivePackage(t *testing.T) {
	root := writeFilesetFixture(t, "      ./pkg/a")
	violations, err := evaluateFilesetCoverage(root)
	if err != nil {
		t.Fatalf("evaluateFilesetCoverage: %v", err)
	}
	assertContainsViolation(t, violations, "pkg/b/b.go")
}

func TestBackendFilesetCoversImports_DetectsDifferenceExcludedImport(t *testing.T) {
	root := writeFilesetFixture(t, `      ./pkg/a
      (lib.fileset.difference ./pkg/b (
        lib.fileset.unions [
          ./pkg/b/skip
          ./pkg/b/b.go
        ]
      ))`)
	violations, err := evaluateFilesetCoverage(root)
	if err != nil {
		t.Fatalf("evaluateFilesetCoverage: %v", err)
	}
	assertContainsViolation(t, violations, "pkg/b/b.go")
}

func TestBackendFilesetCoversImports_AllowsCoveredImports(t *testing.T) {
	root := writeFilesetFixture(t, `      ./pkg/a
      (lib.fileset.difference ./pkg/b (
        lib.fileset.unions [
          ./pkg/b/skip
        ]
      ))`)
	violations, err := evaluateFilesetCoverage(root)
	if err != nil {
		t.Fatalf("evaluateFilesetCoverage: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("flagged a fully covered backend: %v", violations)
	}
}

func TestBackendFilesetCoversImports_FailsLoudlyOnUnparseableFileset(t *testing.T) {
	root := writeFilesetFixture(t, "      (lib.fileset.fileFilter (f: true) ./pkg)")
	violations, err := evaluateFilesetCoverage(root)
	if err != nil {
		t.Fatalf("evaluateFilesetCoverage: %v", err)
	}
	assertContainsViolation(t, violations, "unsupported lib.fileset.fileFilter")

	root = writeFilesetFixture(t, "      ./pkg/does-not-exist")
	violations, err = evaluateFilesetCoverage(root)
	if err != nil {
		t.Fatalf("evaluateFilesetCoverage: %v", err)
	}
	assertContainsViolation(t, violations, "does not exist")
}
