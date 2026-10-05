package main

import (
	"strings"
	"testing"
	"time"
)

var (
	t0      = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	started = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
)

// noGrafanaCtx is the dedupContext for kinds that ignore it.
var noGrafanaCtx = dedupContext{now: t0, stillFiringInterval: 6 * time.Hour}

func TestDecideActionNoMatchCreates(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", State: "active"}
	action, match := decideAction(f, nil, noGrafanaCtx)
	if action != actionCreate || match != nil {
		t.Fatalf("got action=%v match=%v", action, match)
	}
}

func TestDecideActionMatchByAlias(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "new-fp", State: "active"}
	existing := []connectorIssue{{
		ID: "zr-1",
		Metadata: map[string]string{
			metaFingerprint:      "old-fp",
			metaFingerprintAlias: "new-fp;another-fp",
			metaState:            "active",
			metaEpisodeCount:     "0",
		},
	}}
	dc := dedupContext{state: alertState{LastStartsAt: started, LastNoted: t0}, haveState: true, now: t0, stillFiringInterval: 6 * time.Hour}
	f.StartsAt = started
	action, match := decideAction(f, existing, dc)
	if action != actionSkip {
		t.Fatalf("expected actionSkip (nothing changed), got %v", action)
	}
	if match == nil || match.ID != "zr-1" {
		t.Fatalf("expected alias match against zr-1, got %v", match)
	}
}

func TestDecideActionQueueGrowthSkipsSameBand(t *testing.T) {
	f := finding{Kind: kindQueueGrowth, Fingerprint: "queue-growth:backlog", State: "medium"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: "queue-growth:backlog",
		metaState:       "medium",
	}}}
	action, _ := decideAction(f, existing, noGrafanaCtx)
	if action != actionSkip {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionQueueGrowthUpdatesOnNewBand(t *testing.T) {
	f := finding{Kind: kindQueueGrowth, Fingerprint: "queue-growth:backlog", State: "high"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: "queue-growth:backlog",
		metaState:       "medium",
	}}}
	action, _ := decideAction(f, existing, noGrafanaCtx)
	if action != actionUpdate {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionBinaryHashSkipsSameHash(t *testing.T) {
	f := finding{Kind: kindBinaryHashMismatch, Fingerprint: binaryHashMismatchFingerprint, State: "def456"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: binaryHashMismatchFingerprint,
		metaState:       "def456",
	}}}
	action, _ := decideAction(f, existing, noGrafanaCtx)
	if action != actionSkip {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionBinaryHashUpdatesOnNewHash(t *testing.T) {
	f := finding{Kind: kindBinaryHashMismatch, Fingerprint: binaryHashMismatchFingerprint, State: "ghi789"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: binaryHashMismatchFingerprint,
		metaState:       "def456",
	}}}
	action, _ := decideAction(f, existing, noGrafanaCtx)
	if action != actionUpdate {
		t.Fatalf("got %v", action)
	}
}

func TestTrackedMetadataRoundTripsThroughDecideAction(t *testing.T) {
	f := finding{Kind: kindQueueGrowth, Fingerprint: "queue-growth:backlog", State: "medium"}
	tracked := trackedMetadata(f)
	existing := []connectorIssue{{ID: "zr-1", Metadata: tracked}}
	// The exact same finding, matched against the metadata it would have
	// just written, must be a no-op ("nothing new").
	action, _ := decideAction(f, existing, noGrafanaCtx)
	if action != actionSkip {
		t.Fatalf("expected a self-consistent round trip to skip, got %v (tracked=%v)", action, tracked)
	}
}

func TestSplitAndJoinAliasesRoundTrip(t *testing.T) {
	aliases := []string{"a", "b", "c"}
	joined := joinAliases(aliases)
	if joined != "a;b;c" {
		t.Fatalf("got %q", joined)
	}
	got := splitAliases(joined)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitAliasesEmpty(t *testing.T) {
	if got := splitAliases(""); got != nil {
		t.Fatalf("got %v", got)
	}
}

// --- pg2-3tt2e: grafana decision table -------------------------------

const grafanaFP = "pg-router-queue-depth-growing|__alert_rule_uid__=pg-router-queue-depth-growing"

func grafanaFinding(startsAt time.Time) finding {
	return finding{Kind: kindGrafanaAlert, Fingerprint: grafanaFP, Aliases: []string{grafanaFP}, State: "active", StartsAt: startsAt, Values: "B=5"}
}

func openGrafanaBead(id string, labels ...string) connectorIssue {
	return connectorIssue{ID: id, Labels: labels, Metadata: map[string]string{
		metaFingerprint: grafanaFP, metaState: "active", metaEpisodeCount: "0",
	}}
}

func grafanaCtx(lastStarts, lastNoted time.Time, interval time.Duration) dedupContext {
	return dedupContext{state: alertState{LastStartsAt: lastStarts, LastNoted: lastNoted}, haveState: true, now: t0, stillFiringInterval: interval}
}

func TestDecideActionGrafanaTable(t *testing.T) {
	later := started.Add(2 * time.Hour)
	const n = 6 * time.Hour
	cases := []struct {
		name     string
		existing []connectorIssue
		dc       dedupContext
		f        finding
		want     dedupAction
	}{
		{"no match -> create", nil, grafanaCtx(started, t0, n), grafanaFinding(started), actionCreate},
		{"open match + startsAt changed -> recurrence", []connectorIssue{openGrafanaBead("zr-1")}, grafanaCtx(started, t0, n), grafanaFinding(later), actionRecurrence},
		{"open match + same startsAt + last noted exactly N ago -> still firing", []connectorIssue{openGrafanaBead("zr-1")}, grafanaCtx(started, t0.Add(-n), n), grafanaFinding(started), actionStillFiring},
		{"open match + same startsAt + last noted N-1ns ago -> skip", []connectorIssue{openGrafanaBead("zr-1")}, grafanaCtx(started, t0.Add(-n+time.Nanosecond), n), grafanaFinding(started), actionSkip},
		{"open match + same startsAt + noted recently -> skip", []connectorIssue{openGrafanaBead("zr-1")}, grafanaCtx(started, t0.Add(-time.Minute), n), grafanaFinding(started), actionSkip},
		{"open match + no snapshot state -> seed (no comment)", []connectorIssue{openGrafanaBead("zr-1")}, dedupContext{now: t0, stillFiringInterval: n}, grafanaFinding(started), actionSeed},
		{"open human-labeled match behaves the same", []connectorIssue{openGrafanaBead("zr-1", "escalated", "human")}, grafanaCtx(started, t0, n), grafanaFinding(later), actionRecurrence},
		{"missing startsAt never counts as a new episode", []connectorIssue{openGrafanaBead("zr-1")}, grafanaCtx(started, t0, n), grafanaFinding(time.Time{}), actionSkip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, match := decideAction(c.f, c.existing, c.dc)
			if got != c.want {
				t.Fatalf("got action %v, want %v", got, c.want)
			}
			if c.want != actionCreate && (match == nil || match.ID != "zr-1") {
				t.Fatalf("expected the open bead zr-1 to match, got %v", match)
			}
		})
	}
}

func TestNewestClosedMatch(t *testing.T) {
	f := grafanaFinding(started)
	closed := []connectorIssue{
		{ID: "pg2-old", UpdatedAt: "2026-09-29T14:51:00Z", Metadata: map[string]string{metaFingerprint: grafanaFP}},
		{ID: "pg2-other", UpdatedAt: "2026-10-04T00:00:00Z", Metadata: map[string]string{metaFingerprint: "something-else"}},
		{ID: "pg2-new", UpdatedAt: "2026-10-01T10:00:00Z", Metadata: map[string]string{metaFingerprint: grafanaFP}},
		{ID: "pg2-nodate", Metadata: map[string]string{metaFingerprint: grafanaFP}},
	}
	got := newestClosedMatch(f, closed)
	if got == nil || got.ID != "pg2-new" {
		t.Fatalf("got %v, want pg2-new", got)
	}
	if newestClosedMatch(f, nil) != nil {
		t.Fatalf("no closed beads must yield no match")
	}
}

func TestStillFiringCommentText(t *testing.T) {
	f := grafanaFinding(started)
	st := recordEpisode(alertState{}, started, t0)
	got := stillFiringComment(f, st, t0)
	want := "still firing since 2026-10-05T09:00:00Z (3h); episodes in last 7d: 1; current value: B=5"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	f.Values = ""
	if g := stillFiringComment(f, st, t0); !strings.HasSuffix(g, "current value: unknown") {
		t.Fatalf("got %q", g)
	}
}

func TestEpisodesInWindowCountsOnlyLastSevenDays(t *testing.T) {
	st := alertState{
		LastStartsAt: started,
		Episodes: []time.Time{
			t0.Add(-8 * 24 * time.Hour), // outside
			t0.Add(-6 * 24 * time.Hour),
			started,
		},
	}
	if got := episodesInWindow(st, t0); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
	// A long-running alert's own episode always counts.
	old := t0.Add(-30 * 24 * time.Hour)
	if got := episodesInWindow(alertState{LastStartsAt: old, Episodes: []time.Time{old}}, t0); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{
		30 * time.Second:             "0m",
		5 * time.Minute:              "5m",
		2*time.Hour + 30*time.Minute: "2h30m",
		3*24*time.Hour + 4*time.Hour: "3d4h",
		2 * 24 * time.Hour:           "2d",
		-time.Hour:                   "0m",
	} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRecurrenceMetadataIncrementsFromLegacyZero(t *testing.T) {
	f := grafanaFinding(started)
	legacy := openGrafanaBead("zr-1") // episode_count=0, filed before pg2-3tt2e
	if got := recurrenceMetadata(f, &legacy)[metaEpisodeCount]; got != "2" {
		t.Fatalf("legacy bead already covers one episode; got %q, want 2", got)
	}
	legacy.Metadata[metaEpisodeCount] = "4"
	if got := recurrenceMetadata(f, &legacy)[metaEpisodeCount]; got != "5" {
		t.Fatalf("got %q, want 5", got)
	}
}

func TestPruneAlertStates(t *testing.T) {
	old := t0.Add(-10 * 24 * time.Hour)
	states := map[string]alertState{
		"stale":   {LastStartsAt: old, Episodes: []time.Time{old}, LastNoted: old},
		"fresh":   {LastStartsAt: started, Episodes: []time.Time{old, started}, LastNoted: t0},
		"longrun": {LastStartsAt: old, Episodes: []time.Time{old}, LastNoted: t0},
	}
	got := pruneAlertStates(states, t0)
	if _, ok := got["stale"]; ok {
		t.Fatalf("an entry with nothing in the window must be dropped: %+v", got)
	}
	if e := got["fresh"].Episodes; len(e) != 1 || !e[0].Equal(started) {
		t.Fatalf("old episode not pruned: %+v", got["fresh"])
	}
	if e := got["longrun"].Episodes; len(e) != 1 {
		t.Fatalf("a recently-noted long-running alert keeps its current episode: %+v", got["longrun"])
	}
	if len(states["fresh"].Episodes) != 2 {
		t.Fatalf("input must not be mutated")
	}
}
