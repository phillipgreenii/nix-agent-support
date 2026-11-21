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
			for _, want := range []string{"o/r", "not configured"} {
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

func twoRepoCfg() *config.Config {
	return &config.Config{Repos: []config.RepoConfig{
		{Remote: "ZR-Private/ziprecruiter"},
		{Remote: "phillipgziprecruiter/pending-review-scratch"},
	}}
}

// A <pr> naming a configured non-primary repository resolves against it; a
// bare number resolves against the primary; an unconfigured one is rejected.
func TestResolvePRRefTwoRepos(t *testing.T) {
	cfg := twoRepoCfg()
	cases := []struct{ ref, repo, id string }{
		{"5", "ZR-Private/ziprecruiter", "ZR-Private/ziprecruiter#5"},
		{"phillipgziprecruiter/PENDING-review-scratch#5", "phillipgziprecruiter/pending-review-scratch", "phillipgziprecruiter/pending-review-scratch#5"},
		{"https://example.test/zr-private/ziprecruiter/pull/9", "ZR-Private/ziprecruiter", "ZR-Private/ziprecruiter#9"},
	}
	for _, tc := range cases {
		repo, id, err := resolvePRRef(cfg, tc.ref)
		if err != nil || repo != tc.repo || id != tc.id {
			t.Errorf("resolvePRRef(%q) = (%q, %q, %v), want (%q, %q)", tc.ref, repo, id, err, tc.repo, tc.id)
		}
	}
	if _, _, err := resolvePRRef(cfg, "other/repo#5"); err == nil || !strings.Contains(err.Error(), "ZR-Private/ziprecruiter") {
		t.Errorf("unconfigured repo err = %v", err)
	}
}

// --repo makes a non-primary repository primary, so a bare number resolves
// against it; an unconfigured name is an error naming the flag.
func TestSelectRepo(t *testing.T) {
	cfg := twoRepoCfg()
	same, err := selectRepo(cfg, "")
	if err != nil || same != cfg {
		t.Fatalf("no flag: (%v, %v)", same, err)
	}
	sel, err := selectRepo(cfg, "phillipgziprecruiter/pending-review-scratch")
	if err != nil {
		t.Fatalf("selectRepo: %v", err)
	}
	repo, id, err := resolvePRRef(sel, "3")
	if err != nil || repo != "phillipgziprecruiter/pending-review-scratch" || id != "phillipgziprecruiter/pending-review-scratch#3" {
		t.Fatalf("bare number under --repo = (%q, %q, %v)", repo, id, err)
	}
	if _, err := selectRepo(cfg, "nope/nope"); err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("unconfigured --repo err = %v", err)
	}
}

// A one-repository configuration behaves as before: --repo naming it is a
// no-op and a bare number resolves against it.
func TestSelectRepoSingleRepoUnchanged(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	sel, err := selectRepo(cfg, "O/R")
	if err != nil || sel != cfg {
		t.Fatalf("selectRepo = (%v, %v), want the same config", sel, err)
	}
	if repo, id, err := resolvePRRef(sel, "4"); err != nil || repo != "o/r" || id != "o/r#4" {
		t.Fatalf("resolvePRRef = (%q, %q, %v)", repo, id, err)
	}
}
