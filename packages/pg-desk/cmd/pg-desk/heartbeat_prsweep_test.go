package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of the heartbeat's PR age sweep over a real on-disk store and a fake
// hydrator; the clock is injected through heartbeatClock.

var prSweepNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func withPRSweepClock(t *testing.T) {
	t.Helper()
	orig := heartbeatClock
	t.Cleanup(func() { heartbeatClock = orig })
	heartbeatClock = func() time.Time { return prSweepNow }
}

// seedAnchoredPR seeds PR n, hydrated at hydratedAt, with one open anchor.
func seedAnchoredPR(t *testing.T, rs *riStore, n int, hydratedAt time.Time) {
	t.Helper()
	id := riID(n)
	rs.exec(t, `INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash, first_seen_at, hydrated_at)
	            VALUES ('o/r', 'pr', ?, ?, '2026-11-01T00:00:00Z', 'h', '2026-10-01T00:00:00Z', ?)`,
		id, riFacts("open", false, "me"), hydratedAt.Format(time.RFC3339))
	rs.exec(t, `INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash, active)
	            VALUES ('o/r', 'issue', ?, ?, '2026-11-01T00:00:00Z', 'h', 1)`,
		fmt.Sprintf("a%d", n), fmt.Sprintf(`{"issue_show":{"issue_type":%q,"state":"open"}}`, store.AnchorIssueType))
	rs.exec(t, `INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, first_seen, last_confirmed)
	            VALUES ('o/r', 'issue', ?, 'pr', ?, 'derived:work-item', 'work', 't', 't')`, fmt.Sprintf("a%d", n), id)
}

func runPRSweepPass(t *testing.T, rs *riStore, max int) prSweepOutcome {
	t.Helper()
	st, err := store.Open(rs.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cmd, _ := newCatchUpCmd(context.Background())
	out, err := prSweep(context.Background(), cmd, st, max)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Only a PR hydrated longer than sweep.max_age ago is re-hydrated, oldest
// first, through the refresh path with the changed kind, up to the row cap.
func TestPRSweepRehydratesOldestDueFirstUpToTheCap(t *testing.T) {
	withPRSweepClock(t)
	rs := newRIStore(t)
	maxAge := config.DefaultSweepMaxAge
	seedAnchoredPR(t, rs, 1, prSweepNow.Add(-maxAge-2*time.Hour))
	seedAnchoredPR(t, rs, 2, prSweepNow.Add(-maxAge-5*time.Hour))
	seedAnchoredPR(t, rs, 3, prSweepNow.Add(-time.Hour)) // fresh: left alone
	seedAnchoredPR(t, rs, 4, prSweepNow.Add(-maxAge-time.Hour))
	h := &fakeHydrator{res: pipeline.EntityChangeResult{Written: true, Version: 2}}
	withFakeHydrator(t, h)

	out := runPRSweepPass(t, rs, 2)
	if out.selected != 2 || out.refreshed != 2 || out.failed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if fmt.Sprint(h.calls) != "[pr o/r#2 pr o/r#1]" {
		t.Errorf("hydrated %v, want the two oldest due, oldest first", h.calls)
	}
	if h.change != gather.ChangeChanged {
		t.Errorf("change kind = %q, want the refresh path's %q", h.change, gather.ChangeChanged)
	}
}

// A failing hydration is counted, leaves the PR's snapshot alone and steps it
// aside for the retry window, so the next pass reaches the PR behind it.
func TestPRSweepFailingPRStepsAside(t *testing.T) {
	withPRSweepClock(t)
	rs := newRIStore(t)
	old := prSweepNow.Add(-config.DefaultSweepMaxAge - 48*time.Hour)
	seedAnchoredPR(t, rs, 1, old)
	seedAnchoredPR(t, rs, 2, old.Add(time.Hour))
	h := &fakeHydrator{err: fmt.Errorf("pg-connector [pr show]: exit 1: unavailable: 502")}
	withFakeHydrator(t, h)

	out := runPRSweepPass(t, rs, 1)
	if out.selected != 1 || out.failed != 1 || out.refreshed != 0 {
		t.Fatalf("first pass = %+v", out)
	}
	runPRSweepPass(t, rs, 1)
	if fmt.Sprint(h.calls) != "[pr o/r#1 pr o/r#2]" {
		t.Errorf("hydrated %v: the failing PR must step aside for the next pass", h.calls)
	}
}

// Nothing due: no hydration at all.
func TestPRSweepIdleWhenNothingIsDue(t *testing.T) {
	withPRSweepClock(t)
	rs := newRIStore(t)
	seedAnchoredPR(t, rs, 1, prSweepNow.Add(-time.Hour))
	h := &fakeHydrator{}
	withFakeHydrator(t, h)
	if out := runPRSweepPass(t, rs, PRSweepMaxRows); out.selected != 0 || len(h.calls) != 0 {
		t.Errorf("outcome = %+v calls = %v", out, h.calls)
	}
}

// A dev build, or a period spent by earlier steps, skips the sweep.
func TestRunPRSweepSkips(t *testing.T) {
	rs := newRIStore(t)
	seedAnchoredPR(t, rs, 1, prSweepNow.Add(-config.DefaultSweepMaxAge-time.Hour))
	h := &fakeHydrator{}
	withFakeHydrator(t, h)
	st, err := store.Open(rs.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	// dev build
	cmd, errb := newCatchUpCmd(context.Background())
	runPRSweep(cmd, st, prSweepNow)
	if len(h.calls) != 0 || errb.Len() != 0 {
		t.Errorf("dev build: calls=%v stderr=%q", h.calls, errb.String())
	}

	// spent period
	withReleaseVersion(t)
	withHeartbeatClock(t, 50*time.Second)
	start := heartbeatClock() // the heartbeat's start; later reads are 50s on
	cmd, errb = newCatchUpCmd(context.Background())
	runPRSweep(cmd, st, start)
	if len(h.calls) != 0 || !bytes.Contains(errb.Bytes(), []byte("pr age sweep skipped")) {
		t.Errorf("spent period: calls=%v stderr=%q", h.calls, errb.String())
	}
}
