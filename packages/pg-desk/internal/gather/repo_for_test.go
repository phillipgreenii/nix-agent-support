package gather

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func TestGathererRepoForAndBeadsDir(t *testing.T) {
	g := &Gatherer{cfg: &config.Config{Repos: []config.RepoConfig{
		{Remote: "ZR-Private/ziprecruiter", BeadsDir: "/b/zr"},
		{Remote: "phillipgziprecruiter/pending-review-scratch"},
	}}}
	if got := g.repoFor("pr", "phillipgziprecruiter/pending-review-scratch#2"); got != "phillipgziprecruiter/pending-review-scratch" {
		t.Errorf("repoFor second = %q", got)
	}
	if g.repo() != "ZR-Private/ziprecruiter" {
		t.Errorf("repo = %q", g.repo())
	}
	want := []string{"PG_CONNECTOR_ISSUE_BEADS_DIR=/b/zr"}
	if got := g.issueBeadsDirEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("primary env = %v", got)
	}
	if got := g.issueBeadsDirEnvFor("zr-private/ziprecruiter"); !reflect.DeepEqual(got, want) {
		t.Errorf("primary env by name = %v", got)
	}
	if got := g.issueBeadsDirEnvFor("phillipgziprecruiter/pending-review-scratch"); got != nil {
		t.Errorf("second repo sets no beads_dir, got %v", got)
	}
}
