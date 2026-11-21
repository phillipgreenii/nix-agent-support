package pipeline

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func twoRepoPipelineCfg() *config.Config {
	return &config.Config{Repos: []config.RepoConfig{
		{Remote: "ZR-Private/ziprecruiter"},
		{Remote: "phillipgziprecruiter/pending-review-scratch"},
	}}
}

// A pull request is keyed by the configured repository its id names; every
// other entity, and an id naming no configured repository, by the primary.
func TestPipelineRepoFor(t *testing.T) {
	p := &Pipeline{cfg: twoRepoPipelineCfg()}
	cases := []struct{ typ, id, want string }{
		{"pr", "phillipgziprecruiter/pending-review-scratch#3", "phillipgziprecruiter/pending-review-scratch"},
		{"pr", "zr-private/ZipRecruiter#3", "ZR-Private/ziprecruiter"},
		{"pr", "elsewhere/x#3", "ZR-Private/ziprecruiter"},
		{"issue", "phillipgziprecruiter/pending-review-scratch#3", "ZR-Private/ziprecruiter"},
	}
	for _, tc := range cases {
		if got := p.repoFor(tc.typ, tc.id); got != tc.want {
			t.Errorf("repoFor(%s, %s) = %q, want %q", tc.typ, tc.id, got, tc.want)
		}
	}
}

// With one repository every entity is keyed by it, as before.
func TestPipelineRepoForSingleRepo(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}}
	if p.repoFor("pr", "o/r#1") != "o/r" || p.repoFor("pr", "x/y#1") != "o/r" || p.repo() != "o/r" {
		t.Fatal("single repository must key everything by it")
	}
}
