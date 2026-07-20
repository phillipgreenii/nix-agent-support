package poller

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
)

// TestProducer_PublishesRepoLabels is the C1 gate: the producer computes the
// workspace.repo label (owning the provider Cache) and publishes a cwd->label
// map in DerivedState, and RepoLabelSource reads THAT map — so the tick's label
// pipeline never calls provider.Cache.RepoLabel.
func TestProducer_PublishesRepoLabels(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	p := newMonitorPoller(sessionsDir, home, pidAlive, now)
	pc := provider.New(func() time.Time { return now })
	pc.FetchRepoLabel = func(cwd string) (string, bool) {
		if cwd == "/w/a" {
			return "github.com/o/a", true
		}
		return "", false // /w/b etc. are not repos
	}
	p.Providers = pc
	prod := p.Producer()

	ds, err := prod.Assemble(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := ds.RepoLabels["/w/a"]; got != "github.com/o/a" {
		t.Errorf("RepoLabels[/w/a] = %q, want github.com/o/a", got)
	}
	if _, ok := ds.RepoLabel("/w/b"); ok {
		t.Errorf("non-repo cwd /w/b must be absent from RepoLabels")
	}

	prod.Publish(ds)
	src := RepoLabelSource{Prod: prod}
	if v, ok := src.RepoLabel("/w/a"); !ok || v != "github.com/o/a" {
		t.Errorf("RepoLabelSource.RepoLabel(/w/a) = (%q,%v), want (github.com/o/a,true)", v, ok)
	}
	if _, ok := src.RepoLabel("/w/b"); ok {
		t.Errorf("RepoLabelSource.RepoLabel(/w/b) must be false")
	}
}
