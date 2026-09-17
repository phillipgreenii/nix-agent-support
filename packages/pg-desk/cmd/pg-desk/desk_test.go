package main

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// TestResolvePRRefReturnsQualifiedEntityID is the direct regression test for
// pg2-276sg: resolvePRRef must reconstruct the FULL "<repo>#<n>" entity id —
// the form internal/gather/gather.go's Gather actually writes into the
// entity/interpretation tables via pg-connector's own `pr show <id>`
// convention (confirmed empirically against the live store) — not the bare
// number parsePRNumber extracts. Before the fix, resolvePRRef returned the
// bare number for every one of the three documented input forms, so
// show/hide/wip/feedback could never resolve a real, currently-tracked PR.
//
// Every case below must resolve to the SAME qualified id, "o/r#123",
// regardless of which owner/repo the input itself names: Phase 9 supports
// exactly one configured repository (docs/behavior/pg-desk/README.md's
// "Scope"), so resolvePRRef always rebuilds the id from cfg.Repos[0].Remote
// (per parsePRNumber's own doc comment), never from the input's own
// owner/repo portion.
func TestResolvePRRefReturnsQualifiedEntityID(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}

	cases := []string{
		"123",
		"o/r#123",
		"someone-else/other-repo#123",
		"https://example.test/o/r/pull/123",
		"https://example.test/o/r/pull/123/",
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			repo, id, err := resolvePRRef(cfg, ref)
			if err != nil {
				t.Fatalf("resolvePRRef(%q): %v", ref, err)
			}
			if repo != "o/r" {
				t.Errorf("repo = %q, want %q", repo, "o/r")
			}
			if id != "o/r#123" {
				t.Errorf("entityID = %q, want %q", id, "o/r#123")
			}
		})
	}
}

// TestResolvePRRefNoRepoConfigured proves resolvePRRef still fails outright
// (rather than panicking or returning a malformed id) when no repository is
// configured at all.
func TestResolvePRRefNoRepoConfigured(t *testing.T) {
	if _, _, err := resolvePRRef(&config.Config{}, "123"); err == nil {
		t.Fatal("resolvePRRef with no repos configured: error = nil, want an error")
	}
	if _, _, err := resolvePRRef(nil, "123"); err == nil {
		t.Fatal("resolvePRRef with a nil config: error = nil, want an error")
	}
}

// TestResolvePRRefRejectsUnparseablePRRef proves a ref matching none of the
// three documented forms still fails with a helpful error, unaffected by
// the qualified-id reconstruction.
func TestResolvePRRefRejectsUnparseablePRRef(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	if _, _, err := resolvePRRef(cfg, "not-a-pr-ref"); err == nil {
		t.Fatal(`resolvePRRef("not-a-pr-ref"): error = nil, want an error`)
	}
}
