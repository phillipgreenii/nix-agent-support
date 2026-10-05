package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Subject: how runProbe turns Grafana alert episodes (startsAt) into bead
// writes -- the decision table of dedup.go end to end through a spy
// connector, a real snapshot file, and an injected clock (pg2-3tt2e).

const episodeFP = "pg-router-queue-depth-growing|__alert_rule_uid__=pg-router-queue-depth-growing"

func episodeAlert(startsAt time.Time, values string) []grafanaAlert {
	return []grafanaAlert{{
		RuleUID:  "pg-router-queue-depth-growing",
		Labels:   map[string]string{"__alert_rule_uid__": "pg-router-queue-depth-growing"},
		State:    "active",
		StartsAt: startsAt,
		Values:   values,
	}}
}

func episodeSpy(startsAt time.Time, values string, existing, closed []connectorIssue) *spyDeps {
	return &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return episodeAlert(startsAt, values), nil
		},
		listEscalatedByQuery: map[string][]connectorIssue{
			defaultDedupQuery:  existing,
			"escalated-closed": closed,
		},
	}
}

func episodeOpts(t *testing.T) runOptions {
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	opts.stillFiringInterval = 6 * time.Hour
	return opts
}

func openBead(id string, labels ...string) connectorIssue {
	if len(labels) == 0 {
		labels = []string{"escalated"}
	}
	return connectorIssue{ID: id, Labels: labels, Metadata: map[string]string{
		metaFingerprint: episodeFP, metaState: "active", metaEpisodeCount: "0",
	}}
}

func closedBead(id, updatedAt string, labels ...string) connectorIssue {
	b := openBead(id, labels...)
	b.UpdatedAt = updatedAt
	return b
}

// seedAlertState writes a snapshot holding only the given alert state.
func seedAlertState(t *testing.T, path, fp string, st alertState) {
	t.Helper()
	if err := saveSnapshot(path, snapshot{Alerts: map[string]alertState{fp: st}}); err != nil {
		t.Fatal(err)
	}
}

func tick(t *testing.T, opts runOptions, spy *spyDeps, at time.Time) error {
	t.Helper()
	cmd, _ := testCmd()
	return runProbe(cmd, opts, spy.toRunDeps(at))
}

// startsAt change between two consecutive active ticks -> recurrence
// comment (exact Work 5 text), episode++ on the bead's metadata.
func TestEpisodeRecurrenceBetweenConsecutiveTicks(t *testing.T) {
	opts := episodeOpts(t)
	bead := openBead("zr-1")
	start1 := t0.Add(-3 * time.Hour)
	start2 := t0.Add(-30 * time.Minute)

	// Tick 1: first sight of an open bead -> seeded, silent.
	spy1 := episodeSpy(start1, "B=5", []connectorIssue{bead}, nil)
	if err := tick(t, opts, spy1, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(spy1.commented)+len(spy1.updated)+len(spy1.created) != 0 {
		t.Fatalf("seeding tick must write nothing: %+v", spy1)
	}

	// Tick 2: the alert resolved and re-fired in between -> new startsAt.
	spy2 := episodeSpy(start2, "B=7, C=1", []connectorIssue{bead}, nil)
	if err := tick(t, opts, spy2, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy2.created) != 0 {
		t.Fatalf("recurrence must not create a bead: %+v", spy2.created)
	}
	if len(spy2.commented) != 1 || spy2.commented[0].id != "zr-1" {
		t.Fatalf("expected one recurrence comment on zr-1, got %+v", spy2.commented)
	}
	want := "still firing since " + start2.Format(time.RFC3339) + " (30m); episodes in last 7d: 2; current value: B=7, C=1"
	if spy2.commented[0].body != want {
		t.Fatalf("comment text\n got %q\nwant %q", spy2.commented[0].body, want)
	}
	if len(spy2.updated) != 1 || spy2.updated[0].metadata[metaEpisodeCount] != "2" {
		t.Fatalf("expected episode_count 2, got %+v", spy2.updated)
	}

	// Tick 3, same episode, 1 minute later: nothing new.
	spy3 := episodeSpy(start2, "B=7", []connectorIssue{bead}, nil)
	if err := tick(t, opts, spy3, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(spy3.commented)+len(spy3.updated)+len(spy3.created) != 0 {
		t.Fatalf("same episode inside the throttle must be silent: %+v", spy3)
	}
}

// Throttle boundary at exactly N and N-1, with an injected clock.
func TestStillFiringThrottleBoundary(t *testing.T) {
	const n = 6 * time.Hour
	for _, c := range []struct {
		name      string
		sinceNote time.Duration
		wantNote  bool
	}{
		{"exactly N", n, true},
		{"N minus 1ns", n - time.Nanosecond, false},
		{"N plus 1m", n + time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := episodeOpts(t)
			seedAlertState(t, opts.snapshotPath, episodeFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0.Add(-c.sinceNote)})
			spy := episodeSpy(started, "B=5", []connectorIssue{openBead("zr-1")}, nil)
			if err := tick(t, opts, spy, t0); err != nil {
				t.Fatal(err)
			}
			if got := len(spy.commented) == 1; got != c.wantNote {
				t.Fatalf("commented=%v want %v: %+v", got, c.wantNote, spy.commented)
			}
			if len(spy.updated) != 0 {
				t.Fatalf("a still-firing note is comment-only, got metadata writes %+v", spy.updated)
			}
			if c.wantNote {
				want := "still firing since 2026-10-05T09:00:00Z (3h); episodes in last 7d: 1; current value: B=5"
				if spy.commented[0].body != want {
					t.Fatalf("got %q want %q", spy.commented[0].body, want)
				}
				// LastNoted advanced: a second tick right after is silent.
				spy2 := episodeSpy(started, "B=5", []connectorIssue{openBead("zr-1")}, nil)
				if err := tick(t, opts, spy2, t0.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				if len(spy2.commented) != 0 {
					t.Fatalf("expected the throttle to restart after a note, got %+v", spy2.commented)
				}
			}
		})
	}
}

// Work 7 DECISION (accept the re-surface): a still-firing comment IS
// written on an open, non-human escalated bead (it will re-emit on
// escalated-work), and equally on a human-labeled one (item 8, beads
// created escalated+human by pg2-x7ie2).
func TestStillFiringCommentsOnHumanAndNonHumanBeads(t *testing.T) {
	for _, labels := range [][]string{{"escalated"}, {"escalated", "human"}} {
		t.Run(strings.Join(labels, "+"), func(t *testing.T) {
			opts := episodeOpts(t)
			seedAlertState(t, opts.snapshotPath, episodeFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0.Add(-7 * time.Hour)})
			spy := episodeSpy(started, "B=5", []connectorIssue{openBead("zr-1", labels...)}, nil)
			if err := tick(t, opts, spy, t0); err != nil {
				t.Fatal(err)
			}
			if len(spy.commented) != 1 || spy.commented[0].id != "zr-1" || len(spy.created) != 0 {
				t.Fatalf("expected one still-firing comment and no create, got commented=%+v created=%+v", spy.commented, spy.created)
			}
		})
	}
}

// First deploy: an open bead carrying episode_count=0 and no snapshot
// state is seeded WITHOUT a comment (no burst), and the seed holds.
func TestFirstDeploySeedsOpenBeadWithoutCommenting(t *testing.T) {
	opts := episodeOpts(t)
	spy := episodeSpy(started, "B=5", []connectorIssue{openBead("zr-1")}, nil)
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.commented)+len(spy.updated)+len(spy.created) != 0 {
		t.Fatalf("seeding must not write to any bead: %+v", spy)
	}
	snap, ok := loadSnapshot(opts.snapshotPath)
	if !ok {
		t.Fatal("expected a snapshot")
	}
	st, ok := snap.Alerts[episodeFP]
	if !ok || !st.LastStartsAt.Equal(started) || !st.LastNoted.Equal(t0) || len(st.Episodes) != 1 {
		t.Fatalf("seeded state = %+v (ok=%v)", st, ok)
	}
}

func TestCreateRecordsStateSoNextTickIsQuiet(t *testing.T) {
	opts := episodeOpts(t)
	spy := episodeSpy(started, "B=5", nil, nil)
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected a create, got %+v", spy.created)
	}
	if spy.created[0].metadata[metaEpisodeCount] != "1" {
		t.Fatalf("a new bead covers one episode, got %v", spy.created[0].metadata)
	}
	// The next tick sees the bead (open) with the same startsAt.
	bead := openBead("zr-new")
	spy2 := episodeSpy(started, "B=5", []connectorIssue{bead}, nil)
	if err := tick(t, opts, spy2, t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(spy2.commented)+len(spy2.updated)+len(spy2.created) != 0 {
		t.Fatalf("expected silence right after create: %+v", spy2)
	}
}

// A failed comment must not be recorded as noted: the next tick retries.
func TestFailedRecurrenceCommentIsRetried(t *testing.T) {
	opts := episodeOpts(t)
	seedAlertState(t, opts.snapshotPath, episodeFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0.Add(-time.Hour)})
	later := started.Add(time.Hour)
	spy := episodeSpy(later, "B=5", []connectorIssue{openBead("zr-1")}, nil)
	spy.commentErr = errUnreachable
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.updated) != 0 {
		t.Fatalf("metadata must not be bumped when the comment failed: %+v", spy.updated)
	}
	spy2 := episodeSpy(later, "B=5", []connectorIssue{openBead("zr-1")}, nil)
	if err := tick(t, opts, spy2, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(spy2.commented) != 1 {
		t.Fatalf("expected the recurrence to be retried, got %+v", spy2.commented)
	}
}

// Closed-only match -> a NEW bead whose body names the newest closed id.
func TestClosedOnlyMatchCreatesBeadReferencingNewestClosed(t *testing.T) {
	opts := episodeOpts(t)
	opts.closedDedupQuery = "escalated-closed"
	closed := []connectorIssue{
		closedBead("pg2-pqw1u", "2026-09-29T14:51:00Z"),
		closedBead("pg2-cuvo0", "2026-09-29T14:52:00Z", "escalated", "human"),
		{ID: "pg2-unrelated", UpdatedAt: "2026-10-05T00:00:00Z", Metadata: map[string]string{metaFingerprint: "other"}},
	}
	spy := episodeSpy(started, "B=5", nil, closed)
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected a new bead, got %+v", spy.created)
	}
	if !strings.Contains(spy.created[0].body, "Predecessor: pg2-cuvo0 (closed") {
		t.Fatalf("body must reference the newest closed predecessor, got %q", spy.created[0].body)
	}
	if strings.Contains(spy.created[0].body, "pg2-pqw1u") || strings.Contains(spy.created[0].body, "pg2-unrelated") {
		t.Fatalf("body must reference only the newest matching closed bead: %q", spy.created[0].body)
	}
}

// Mixed open + closed matches -> the open one wins: no create, and the
// closed list is never even consulted.
func TestOpenMatchWinsOverClosedMatch(t *testing.T) {
	opts := episodeOpts(t)
	opts.closedDedupQuery = "escalated-closed"
	seedAlertState(t, opts.snapshotPath, episodeFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0})
	spy := episodeSpy(started.Add(time.Hour), "B=5", []connectorIssue{openBead("zr-open")}, []connectorIssue{closedBead("pg2-old", "2026-09-29T14:51:00Z")})
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("an open match must suppress creation: %+v", spy.created)
	}
	if len(spy.commented) != 1 || spy.commented[0].id != "zr-open" {
		t.Fatalf("expected the recurrence on the open bead, got %+v", spy.commented)
	}
	for _, q := range spy.listedQueries {
		if q == "escalated-closed" {
			t.Fatalf("closed query must not run when every finding has an open match: %v", spy.listedQueries)
		}
	}
}

// A closed-query failure degrades to exit 4 and still files the new bead,
// without the reference.
func TestClosedQueryFailureDegradesButStillFilesBead(t *testing.T) {
	opts := episodeOpts(t)
	opts.closedDedupQuery = "escalated-closed"
	cmd, stderr := testCmd()
	spy := episodeSpy(started, "B=5", nil, nil)
	deps := spy.toRunDeps(t0)
	base := deps.listEscalated
	deps.listEscalated = func(ctx context.Context, query string, warn func(string)) ([]connectorIssue, error) {
		if query == "escalated-closed" {
			return nil, errConnectorFailed
		}
		return base(ctx, query, warn)
	}
	err := runProbe(cmd, opts, deps)
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (degraded), got %v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("the bead must still be filed, got %+v", spy.created)
	}
	if strings.Contains(spy.created[0].body, "Predecessor:") {
		t.Fatalf("no reference is possible without the closed list: %q", spy.created[0].body)
	}
	if !strings.Contains(stderr.String(), "closed-dedup query") {
		t.Fatalf("expected the failure on stderr, got %q", stderr.String())
	}
}

// With no --closed-dedup-query the lookup is off and nothing extra runs.
func TestNoClosedQueryConfiguredNeverListsClosed(t *testing.T) {
	opts := episodeOpts(t)
	spy := episodeSpy(started, "B=5", nil, []connectorIssue{closedBead("pg2-old", "2026-09-29T14:51:00Z")})
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.listedQueries) != 1 || len(spy.created) != 1 || strings.Contains(spy.created[0].body, "Predecessor:") {
		t.Fatalf("listed=%v created=%+v", spy.listedQueries, spy.created)
	}
}

// The persisted snapshot stays free of episode data when there is nothing
// grafana-shaped to remember (omitempty keeps old readers/writers aligned).
func TestRunCmdNewFlagDefaults(t *testing.T) {
	cmd := newRunCmd()
	if f := cmd.Flags().Lookup("closed-dedup-query"); f == nil || f.DefValue != "" {
		t.Fatalf("--closed-dedup-query missing or has a default: %+v", f)
	}
	f := cmd.Flags().Lookup("still-firing-interval")
	if f == nil || f.DefValue != "6h0m0s" {
		t.Fatalf("--still-firing-interval default = %+v, want 6h0m0s", f)
	}
}

func TestStillFiringIntervalRejectsNegative(t *testing.T) {
	var stderrBuf, stdoutBuf strings.Builder
	code := run([]string{"run", "--still-firing-interval=-1h", "--grafana-url=http://example.invalid", "--snapshot-path", filepath.Join(t.TempDir(), "s.json")}, &stdoutBuf, &stderrBuf)
	if code != 2 {
		t.Fatalf("exit %d, stderr %q", code, stderrBuf.String())
	}
}
