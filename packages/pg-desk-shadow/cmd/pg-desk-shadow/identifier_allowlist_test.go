package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file is pg-desk-shadow's OWN copy of the mechanical guard for bead
// pg2-tphcc (see packages/pg-pr/cmd/pg-pr/identifier_allowlist_test.go for the
// rationale: an ALLOWLIST of known-safe identifiers, never a denylist of
// forbidden ones, per the operator ruling of 2026-08-24). The guard scans only
// testdata/ trees under this module's root, so a copy lives here. Fixtures in
// this module are synthetic (acme/api#N, teammate); the only identity allowed
// is the operator's own.
var allowlistedIdentifiers = map[string]struct{}{
	"phillipgreenii":            {},
	"phillipg@ziprecruiter.com": {},
	"teammate":                  {},
	"review-bot":                {},
	"tester":                    {},
}

var (
	identityFieldPattern = regexp.MustCompile(`"(?:login|author|reviewer|user)"\s*:\s*"([^"]*)"`)
	branchLoginPattern   = regexp.MustCompile(`\b([a-z][a-z0-9-]*\.[a-z][a-z0-9-]*)\.[A-Z][A-Z0-9]*-[0-9]+\b`)
	gitTrailerPattern    = regexp.MustCompile(`(?m)^\s*(?:Author|Committer):\s*([^<\n]+?)\s*<`)
)

func scanTextForIdentifiers(text string) []string {
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := allowlistedIdentifiers[strings.ToLower(id)]; ok {
			return
		}
		seen[id] = struct{}{}
	}
	for _, m := range identityFieldPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range branchLoginPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range gitTrailerPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// moduleRoot walks up to this module's go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// TestIdentifierAllowlistGuard scans every testdata/ tree of this module.
func TestIdentifierAllowlistGuard(t *testing.T) {
	root := moduleRoot(t)
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(filepath.ToSlash(path), "/testdata/") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		if bad := scanTextForIdentifiers(string(b)); len(bad) > 0 {
			t.Errorf("%s holds identifier(s) not on the allowlist: %v", path, bad)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("the guard scanned no testdata files: it would pass vacuously")
	}
}

func TestIdentifierGuardCatchesAndAllowsShapes(t *testing.T) {
	if got := scanTextForIdentifiers(`{"login": "someone-real", "author": "teammate"}`); len(got) != 1 || got[0] != "someone-real" {
		t.Errorf("got %v", got)
	}
	if got := scanTextForIdentifiers("Author: Some Person <a@b.c>"); len(got) != 1 {
		t.Errorf("trailer: %v", got)
	}
	if got := scanTextForIdentifiers("first.last.PROJ-9"); len(got) != 1 {
		t.Errorf("branch: %v", got)
	}
}
