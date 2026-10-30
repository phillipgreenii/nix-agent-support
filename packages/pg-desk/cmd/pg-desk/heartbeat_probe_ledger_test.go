package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// probeQueries is a feed.Queries over two configured, baselined queries.
type probeQueries struct{}

func (probeQueries) Known(n string) bool { return n == "mine" || n == "team" }

func (probeQueries) Names() []string                               { return []string{"mine", "team"} }
func (probeQueries) Watched(string) bool                           { return false }
func (probeQueries) Run(context.Context, string) error             { return nil }
func (probeQueries) WatchedRefs(context.Context) ([]string, error) { return nil, nil }
func (probeQueries) Members(context.Context, string) (feed.Members, error) {
	return feed.Members{Baseline: true}, nil
}

// TestHeartbeatProbeBindsOneConsumerPerQueryOnARealLedger (V-3, pg2-9vaehh)
// drives the probe's `pr changes` calls into pg-connector's real feed over a
// real sqlite store: a consumer binds to one scope on first use, so one shared
// consumer id made the second query fail (exit 3).
func TestHeartbeatProbeBindsOneConsumerPerQueryOnARealLedger(t *testing.T) {
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f, err := feed.New(feed.Config{
		Spec: registry.Spec{
			EntityKind: "pr", PrimaryGroup: "main", Groups: []string{"main"},
			Changes:    []string{"added", "removed", "entered", "left"},
			Roles:      registry.Roles{Added: "added", Removed: "removed", EnteredQuery: "entered", LeftQuery: "left"},
			Views:      []registry.View{{Name: "all"}},
			FoldRef:    func(s string) string { return s },
			Emission:   registry.EmitWatchedOnly,
			Delivery:   registry.DeliverByWatched,
			Membership: registry.MembershipPolicy{StoreRows: true},
		},
		Store: st, Queries: probeQueries{}, Clock: governor.NewManualClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)),
		Retention: 14 * 24 * time.Hour, ConsumerStaleAfter: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	withHeartbeatLedger(t, `[]`, nil)
	orig := heartbeatRunConnector
	t.Cleanup(func() { heartbeatRunConnector = orig })
	var polled []string
	heartbeatRunConnector = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "capabilities" {
			return []byte(prCapabilitiesJSON), nil
		}
		get := func(flag string) string { return args[slices.Index(args, flag)+1] }
		_, err := f.Poll(ctx, feed.PollRequest{Consumer: get("--consumer"), Scope: get("--query"), Cached: true})
		if err == nil {
			polled = append(polled, get("--query"))
		}
		return []byte(`{"changes":[]}`), err
	}

	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if stderr != "" && slices.Contains([]string{"not probed"}, "not probed") && len(polled) != 2 {
		t.Errorf("polled = %v, stderr = %q; want both queries bound and probed", polled, stderr)
	}
	if !slices.Equal(polled, []string{"mine", "team"}) {
		t.Errorf("polled = %v, want [mine team]", polled)
	}
	// A second heartbeat reuses the same bindings.
	polled = nil
	if _, err := runHeartbeat(t); err != nil || len(polled) != 2 {
		t.Errorf("second heartbeat: err=%v polled=%v", err, polled)
	}
}
