package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Subject: rules listed in runOptions.groupRuleUIDs share ONE bead across
// label sets, each label set an episode comment; other rules keep one bead
// per label set.

func burstAlerts(uid string, repos ...string) []grafanaAlert {
	var out []grafanaAlert
	for _, r := range repos {
		out = append(out, grafanaAlert{
			RuleUID:  uid,
			Labels:   map[string]string{"__alert_rule_uid__": uid, "repo": r},
			State:    "active",
			StartsAt: started,
		})
	}
	return out
}

func groupSpy(alerts []grafanaAlert) *spyDeps {
	return &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return alerts, nil
		},
		listEscalatedByQuery: map[string][]connectorIssue{},
	}
}

var sixRepos = []string{"r1", "r2", "r3", "r4", "r5", "r6"}

func TestGroupedRuleBurstYieldsOneBeadAndSixEpisodes(t *testing.T) {
	opts := episodeOpts(t)
	opts.groupRuleUIDs = []string{"pg-repo-runner-tips-stale"}
	spy := groupSpy(burstAlerts("pg-repo-runner-tips-stale", sixRepos...))
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("want 1 bead, got %d", len(spy.created))
	}
	if got := spy.created[0].metadata[metaFingerprint]; got != "group:pg-repo-runner-tips-stale" {
		t.Fatalf("bead fingerprint = %q", got)
	}
	if len(spy.commented) != 6 {
		t.Fatalf("want 6 episode comments, got %d: %+v", len(spy.commented), spy.commented)
	}
	for i, r := range sixRepos {
		c := spy.commented[i]
		if c.id != "zr-new" || !strings.Contains(c.body, "repo="+r) {
			t.Fatalf("comment %d = %+v, want one naming repo=%s on zr-new", i, c, r)
		}
	}
	// The five joins bump the episode count 1..6 on the bead.
	if len(spy.updated) != 5 || spy.updated[4].metadata[metaEpisodeCount] != "6" {
		t.Fatalf("episode count updates = %+v", spy.updated)
	}
}

func TestUngroupedRuleBurstStillOneBeadPerLabelSet(t *testing.T) {
	opts := episodeOpts(t)
	opts.groupRuleUIDs = []string{"pg-repo-runner-tips-stale"}
	spy := groupSpy(burstAlerts("some-other-rule", sixRepos...))
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 6 || len(spy.commented) != 0 {
		t.Fatalf("want 6 beads, 0 comments; got %d, %d", len(spy.created), len(spy.commented))
	}
}

func TestGroupedRuleSecondTickIsSilent(t *testing.T) {
	opts := episodeOpts(t)
	opts.groupRuleUIDs = []string{"pg-repo-runner-tips-stale"}
	alerts := burstAlerts("pg-repo-runner-tips-stale", sixRepos...)
	first := groupSpy(alerts)
	if err := tick(t, opts, first, t0); err != nil {
		t.Fatal(err)
	}
	bead := connectorIssue{ID: "zr-new", Labels: []string{"escalated"}, Metadata: map[string]string{
		metaFingerprint: "group:pg-repo-runner-tips-stale", metaState: "active", metaEpisodeCount: "6",
	}}
	second := groupSpy(alerts)
	second.listEscalatedByQuery[defaultDedupQuery] = []connectorIssue{bead}
	if err := tick(t, opts, second, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(second.created)+len(second.commented)+len(second.updated) != 0 {
		t.Fatalf("second tick must be silent: %+v", second)
	}
}
