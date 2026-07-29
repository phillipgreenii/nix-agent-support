package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// sectionSign is the section-sign rune, built from its code point so that THIS
// FILE never contains the literal character it forbids. That is what lets the
// guard scan itself (and every other file, tests included) without self-tripping
// — do NOT paste the raw glyph into this file or the guard will fail on itself.
const sectionSign = string(rune(0x00a7))

// TestNoEphemeralSpecCitationsUnderCcpool is the durable-citation guard for bead
// pg2-oxrha.
//
// ccpool once carried 46 `spec <section-sign>N` citations across 20 files (19 Go
// files plus default.nix) pointing at two DESIGN SPECS that live OUTSIDE this
// repository — under the pn-workspace root's docs/superpowers/specs/, not in
// phillipgreenii-nix-agent-support at all. Those specs are extraction sources:
// they are used once and then stop being cited, so every reference to them was
// guaranteed to dangle. One citation had already rotted before this guard existed
// (`spec <section-sign>"Counter resets on a successful turn"` named a heading
// present in NEITHER spec), which is the concrete decay this test prevents.
//
// The invariant, enforced mechanically over the whole ccpool module:
//
//	No file under packages/ccpool may contain a section-sign citation. A rule the
//	code depends on MUST be stated in the code itself, or cited to a durable
//	in-repo owner (an ADR by number, e.g. "ADR 0037"), never to a section number
//	in an out-of-repo document.
//
// Two properties matter and are deliberate:
//
//   - It scans EVERY file type, not just Go. The original citation set included
//     `packages/ccpool/default.nix`, so a Go-AST-only or `*.go`-only guard would
//     have left that one unguarded. Markdown, JSON, and testdata are scanned too.
//   - It scans `_test.go` files. Two of the original citations lived in tests
//     (cmd/ccpool/plugin_test.go and internal/session/send_test.go), so excluding
//     tests — as a narrower guard reasonably might — would leave a live hole.
//
// Forbidding the section sign outright, rather than only the exact string
// "spec <section-sign>", is intentional: the original set also contained bare
// (`<section-sign>8.3`) and differently-cased (`Spec <section-sign>8.3`) spellings
// of the same dangling references, and every one of them is caught by the rune.
// In-repo ADRs are cited by number and heading name in this codebase, never with
// a section sign, so nothing legitimate is excluded.
//
// SCOPE BOUNDARY — this test guards the Go MODULE; a companion nix check guards
// the rest of the ccpool surface (bead pg2-qkk8n).
//
// ccpoolModuleRoot walks up to go.mod, and the nix src for this module is rooted
// at `packages/` with modRoot=ccpool (Pattern B), so ccpool's nix modules —
// `home/programs/ccpool/`, `darwin/modules/ccpool/`, `nixos/modules/ccpool/` —
// are outside BOTH this walk and the build sandbox's tree. They are structurally
// unreachable from here, and one of them (`home/programs/ccpool/default.nix`) was
// in fact still carrying a `spec <section-sign>8.1.1/<section-sign>14 step 6`
// citation of exactly this class after pg2-oxrha landed. That half is now guarded
// by `checks.<system>.test-ccpool-surface-spec-citations` in flake.nix, which runs
// the same ban from the repo root; a new ccpool-surface directory must be added to
// that check's fileset. Do not try to reach outside the module from here — the
// sandbox has no repo root to find.
//
// The union of the two guards is the ccpool SURFACE, and that is DELIBERATELY not
// the whole repo: repo-wide the glyph appears 534 times across 146 files (ADR
// prose, historical docs/superpowers/plans/, other packages' own in-repo
// citations), nearly all legitimate, so a repo-wide ban would need an allowlist
// longer than the rule. Elsewhere the convention is documented, not tested — see
// CLAUDE.md "Citation conventions".
func TestNoEphemeralSpecCitationsUnderCcpool(t *testing.T) {
	root := ccpoolModuleRoot(t)

	var violations []string
	scanned := map[string]int{} // file extension -> count
	sawDefaultNix := false
	sawGoTest := false

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		// Build artifacts, not sources. The module's own .gitignore lists these
		// (plus the compiled `ccpool` binary, which the UTF-8 check below drops).
		switch filepath.Ext(name) {
		case ".db", ".out", ".test":
			return nil
		}

		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		// A non-UTF-8 file is a binary (e.g. a stray `go build` output in a dev
		// tree), never a source file carrying a citation.
		if !utf8.Valid(b) {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		ext := filepath.Ext(name)
		if ext == "" {
			ext = "(none)"
		}
		scanned[ext]++
		if rel == "default.nix" {
			sawDefaultNix = true
		}
		if strings.HasSuffix(name, "_test.go") {
			sawGoTest = true
		}

		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, sectionSign) {
				violations = append(violations, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scanning %s: %v", root, walkErr)
	}

	// Self-checks: a guard that silently stopped scanning is worse than no guard,
	// and the expected violation count is zero, so "found nothing" cannot double as
	// proof of life. Assert instead that the walk really covered the file kinds this
	// invariant exists for — Nix and Go tests included, not just plain Go.
	if !sawDefaultNix {
		t.Errorf("guard never scanned default.nix under %s — the non-Go half of the "+
			"invariant is unguarded", root)
	}
	if !sawGoTest {
		t.Errorf("guard never scanned any _test.go file under %s — test files carried "+
			"two of the original citations", root)
	}
	if scanned[".go"] < 20 {
		t.Errorf("guard scanned only %d .go file(s) under %s; the module has far more, "+
			"so the walk is not covering it", scanned[".go"], root)
	}
	exts := make([]string, 0, len(scanned))
	for ext := range scanned {
		exts = append(exts, fmt.Sprintf("%s=%d", ext, scanned[ext]))
	}
	sort.Strings(exts)
	t.Logf("scanned %s under %s", strings.Join(exts, " "), root)

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: forbidden section-sign (%s) citation. ccpool's design specs live "+
			"OUTSIDE this repo and are not a durable record, so a %sN reference dangles. "+
			"State the rule in the comment itself, or cite a durable in-repo owner by "+
			"number (e.g. \"ADR 0037\") and heading name instead.", v, sectionSign, sectionSign)
	}
}

// ccpoolModuleRoot walks up from the test's working directory to the directory
// holding go.mod, so the guard covers the whole ccpool module regardless of which
// package runs it — and works unchanged inside the nix build sandbox, where the
// source is rooted at packages/ with modRoot=ccpool.
func ccpoolModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found at or above %q", dir)
		}
		dir = parent
	}
}
