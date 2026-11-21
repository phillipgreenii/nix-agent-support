package changes

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// The poll reads and writes a listed pull request under the repository its id
// names, and every other entity under the primary repository.
func TestEngineRepoFor(t *testing.T) {
	e := &Engine{Cfg: &config.Config{Repos: []config.RepoConfig{
		{Remote: "ZR-Private/ziprecruiter"},
		{Remote: "phillipgziprecruiter/pending-review-scratch"},
	}}}
	if got := e.repoFor("pr", "phillipgziprecruiter/pending-review-scratch#3"); got != "phillipgziprecruiter/pending-review-scratch" {
		t.Errorf("second repo pr: %q", got)
	}
	if got := e.repoFor("pr", "ZR-Private/ziprecruiter#3"); got != "ZR-Private/ziprecruiter" {
		t.Errorf("primary repo pr: %q", got)
	}
	if got := e.repoFor("issue", "phillipgziprecruiter/pending-review-scratch#3"); got != "ZR-Private/ziprecruiter" || e.repo() != got {
		t.Errorf("issue: %q", got)
	}
	if (&Engine{}).repoFor("pr", "a/b#1") != "" {
		t.Error("no config: no repo")
	}
}
