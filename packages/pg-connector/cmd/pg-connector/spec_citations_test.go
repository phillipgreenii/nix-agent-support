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

// TestNoSectionSignCitationsUnderPgConnector is the durable-citation guard for
// bead pg2-hidkm, mirroring ccpool's own precedent
// (packages/ccpool/cmd/ccpool/spec_citations_test.go, bead pg2-oxrha).
//
// pg-connector once carried roughly 250 section-sign citations across ~65 files
// (Go sources, tests, and the four backend .nix files) pointing at the design
// doc under the pn-workspace root's docs/superpowers/specs/ — a file that lives
// OUTSIDE this repository, is slated for deletion once retired, and is not a
// durable record. A further ~30 citations pointed at a second ephemeral,
// out-of-repo file (a dated review doc at the pn-workspace root, outside every
// repo's git history) using the same forbidden section-sign shape. Both classes
// were rewritten (bead pg2-hidkm) to cite durable, in-repo owners instead —
// ADR 0062, the packages/pg-connector/docs/behavior/ invariant/interface IDs
// (INV-*/INTF-* — the bare-ID citation convention pr-pool's own Go code already
// uses for its behavior docs), or the specific mechanical test that already
// enforces a naming/layout/composition rule, whichever fits. This guard is
// what keeps that rewrite from rotting back to dangling section-sign citations
// one packet at a time.
//
// The invariant, enforced mechanically over the whole pg-connector module:
//
//	No file under packages/pg-connector may contain a section-sign citation. A
//	rule the code depends on MUST be stated in the code itself, or cited to a
//	durable in-repo owner (an ADR by number, a behavior-docs INV-*/INTF-* ID, or
//	a named test), never to a section number in an out-of-repo document.
//
// Two properties matter and are deliberate (same rationale as ccpool's guard):
//
//   - It scans EVERY file type, not just Go. The original citation set included
//     the four backend .nix files (pg-connector-pr-github.nix and siblings), so
//     a Go-AST-only or `*.go`-only guard would have left those unguarded.
//     packages/pg-connector/docs/behavior/*.md and README.md are scanned too —
//     they legitimately carry zero section signs today (INV-*/INTF-* IDs are
//     the citation surface there), and this guard is what keeps it that way.
//   - It scans `_test.go` files. A large share of the original citations lived
//     in tests, so excluding them — as a narrower guard reasonably might —
//     would leave a live hole.
//
// Forbidding the section sign outright, rather than only the exact string
// "design: " followed by the sign, is intentional: the original set also
// contained bare (sign plus a bare section number) and differently-shaped
// ("design" followed by the sign and a number's possessive, or the sign and a
// number's own possessive with no "design" at all) spellings of the same
// dangling references, and every one of them is caught by the rune. In-repo
// ADRs and behavior-docs IDs are cited by number/ID and heading/name in this
// codebase, never with a section sign, so nothing legitimate is excluded.
//
// SCOPE BOUNDARY — unlike ccpool, ONE guard is sufficient for the whole
// pg-connector SURFACE, not two. ccpool needed a companion nix-level check
// (checks.<system>.test-ccpool-surface-spec-citations) because its nix
// CONSUMERS live outside its own Go module (home/programs/ccpool/,
// darwin/modules/ccpool/, nixos/modules/ccpool/), structurally unreachable
// from a test that walks up to go.mod. pg-connector has no such external
// consumer surface: every one of its nix PACKAGING files (default.nix and the
// four backend .nix files) lives directly beside go.mod, inside
// pgConnectorModuleRoot's own walk (verified by the wantNixFiles check
// below) — confirmed by grep: nothing outside packages/pg-connector/ names
// "pg-connector" in home/, darwin/, or nixos/. A future pg-connector-facing
// home-manager module (bead pg2-j3w6i) would need a companion check the same
// way ccpool's does, if and only if it ever carries a section-sign citation of
// its own.
//
// pgConnectorModuleRoot is reused, not redefined: identifier_allowlist_test.go
// (this same package) already declares it, mirroring pgPrModuleRoot in
// packages/pg-pr/cmd/pg-pr/identifier_allowlist_test.go and ccpoolModuleRoot in
// ccpool's own guard.
//
// Repo-wide the glyph still appears hundreds of times across other packages'
// own in-repo citations, ADR prose, and historical docs/superpowers/plans/ —
// nearly all legitimate — so a repo-wide ban would need an allowlist longer
// than the rule. Elsewhere the convention is documented, not tested — see
// CLAUDE.md "Citation conventions".
func TestNoSectionSignCitationsUnderPgConnector(t *testing.T) {
	root := pgConnectorModuleRoot(t)

	var violations []string
	scanned := map[string]int{} // file extension -> count
	sawGoTest := false
	wantNixFiles := map[string]bool{
		"default.nix":                        false,
		"pg-connector-pr-github.nix":         false,
		"pg-connector-ci-github-actions.nix": false,
		"pg-connector-issue-beads.nix":       false,
		"pg-connector-scm-git.nix":           false,
	}

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
		// Build artifacts, not sources. Compiled binaries and coverage/test
		// output are never source files carrying a citation.
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
		if _, want := wantNixFiles[rel]; want {
			wantNixFiles[rel] = true
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

	// Self-checks: a guard that silently stopped scanning is worse than no
	// guard, and the expected violation count is zero, so "found nothing"
	// cannot double as proof of life. Assert instead that the walk really
	// covered the file kinds this invariant exists for — every backend's own
	// nix packaging file and Go tests included, not just plain Go.
	for rel, saw := range wantNixFiles {
		if !saw {
			t.Errorf("guard never scanned %s under %s — the non-Go half of the "+
				"invariant is unguarded", rel, root)
		}
	}
	if !sawGoTest {
		t.Errorf("guard never scanned any _test.go file under %s — test files carried "+
			"a large share of the original citations", root)
	}
	if scanned[".go"] < 100 {
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
		t.Errorf("%s: forbidden section-sign (%s) citation. The ephemeral design "+
			"spec and review doc this once cited live OUTSIDE this repo and are not "+
			"a durable record, so a %sN reference dangles. State the rule in the "+
			"comment itself, or cite a durable in-repo owner instead: an ADR by "+
			"number (e.g. \"ADR 0062\"), a packages/pg-connector/docs/behavior/ "+
			"INV-*/INTF-* ID, or the specific mechanical test that already enforces "+
			"the rule.", v, sectionSign, sectionSign)
	}
}
