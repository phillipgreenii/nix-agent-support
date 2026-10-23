package query

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The GitHub adapter's side of the watched queries: the flag is read from the
// configuration, and the two-argument Fetch of the feed and the list operation
// stays next to the form that takes the watch option.

func TestSettings_WatchedIsTheConfiguredQueryFlag(t *testing.T) {
	r := newRig(t)
	r.cfg.PR.Queries = map[string]schema.QueryDef{
		"mine": {Expr: schema.QueryExpr{"one"}, Watched: true},
		"team": {Expr: schema.QueryExpr{"two"}},
	}
	s := configSettings{r.cfg}
	if !s.Watched("mine") || s.Watched("team") || s.Watched("unknown") {
		t.Errorf("Watched(mine, team, unknown) = %v, %v, %v, want true, false, false", s.Watched("mine"), s.Watched("team"), s.Watched("unknown"))
	}
}

// fetcher is the two-argument shape that feed.Queries and the list operation
// declare for the runner.
type fetcher interface {
	Fetch(ctx context.Context, key Key) (store.QueryRow, error)
}

var _ fetcher = (*Runner)(nil)

func TestFetch_TheWatchOptionMarksTheMembersOfAGitHubRun(t *testing.T) {
	var eng *changes.Engine
	r := newRigWith(t, func(c *Config) {
		var err error
		if eng, err = changes.New(changes.Config{Clock: c.Clock}); err != nil {
			t.Fatal(err)
		}
		c.Changes = eng
	})
	r.run(t)

	if _, err := r.runner.FetchWith(context.Background(), Key{Name: "mine"}, shared.FetchOptions{Watch: true}); err != nil {
		t.Fatal(err)
	}
	watched, err := r.st.ListWatched(context.Background(), changes.LogKindPR)
	if err != nil {
		t.Fatal(err)
	}
	if len(watched) != 3 {
		t.Fatalf("%d entities are watched, want the three members of the listing", len(watched))
	}
	for _, e := range watched {
		if e.WatchedVia != registry.ViaAdHoc+"mine" {
			t.Errorf("%s watched via %q, want the ad-hoc cause", e.ID, e.WatchedVia)
		}
	}
}
