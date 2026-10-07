package main

import (
	"strings"
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
// Every case below must resolve to the SAME qualified id, "o/r#123": Phase 9
// supports exactly one configured repository (docs/behavior/pg-desk/README.md's
// "Scope"), so resolvePRRef rebuilds the id from cfg.Repos[0].Remote. A ref
// that names a DIFFERENT repository is rejected instead (see
// TestResolvePRRefRejectsNonConfiguredRepo).
func TestResolvePRRefReturnsQualifiedEntityID(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}

	cases := []string{
		"123",
		"#123",
		"o/r#123",
		"O/R#123",
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

// TestResolvePRRefRejectsNonConfiguredRepo is the regression test for
// pg2-5eus1: an explicit OWNER/REPO#N (or PR URL) naming a repository other
// than the configured one used to resolve silently to cfg.Repos[0] — reading
// and possibly hydrating a DIFFERENT repo's PR with the same number. It must
// now fail with an explicit error naming both repositories.
func TestResolvePRRefRejectsNonConfiguredRepo(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}

	for _, ref := range []string{
		"someone-else/other-repo#123",
		"o/other#123",
		"other/r#123",
		"https://example.test/someone-else/other-repo/pull/123",
		"https://example.test/o/other/pull/123/",
	} {
		t.Run(ref, func(t *testing.T) {
			repo, id, err := resolvePRRef(cfg, ref)
			if err == nil {
				t.Fatalf("resolvePRRef(%q) = (%q, %q, nil), want an error", ref, repo, id)
			}
			if repo != "" || id != "" {
				t.Errorf("resolvePRRef(%q) returned (%q, %q) alongside an error, want empty", ref, repo, id)
			}
			for _, want := range []string{"o/r", "only configured repository"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
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
