package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Subject: the `escalation` alert label -- how a rule routes its bead
// straight to the operator (labels escalated + human), and that the routing
// never changes the alert's identity or dedup behavior (pg2-x7ie2).

const originFP = "pg-router-origin-unavailable|__alert_rule_uid__=pg-router-origin-unavailable,severity=warning"

func originAlert(escalation string) grafanaAlert {
	labels := map[string]string{"__alert_rule_uid__": "pg-router-origin-unavailable", "severity": "warning"}
	if escalation != "" {
		labels[escalationLabelKey] = escalation
	}
	return grafanaAlert{
		RuleUID:               "pg-router-origin-unavailable",
		Labels:                labels,
		State:                 "active",
		StartsAt:              started,
		AnnotationSummary:     "origin is unavailable",
		AnnotationDescription: "renew the step cert in an interactive shell",
	}
}

func originSpy(escalation string) *spyDeps {
	return &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{originAlert(escalation)}, nil
		},
	}
}

func TestEscalationHumanCreatesEscalatedAndHumanBead(t *testing.T) {
	opts := episodeOpts(t)
	spy := originSpy("human")
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected one create, got %+v", spy.created)
	}
	if got := spy.created[0].labels; !slices.Equal(got, []string{"escalated", "human"}) {
		t.Fatalf("labels = %v, want [escalated human]", got)
	}
	body := spy.created[0].body
	for _, want := range []string{"Remediation:", "origin is unavailable", "renew the step cert in an interactive shell", "Close this bead once the alert clears"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if got := spy.created[0].metadata[metaFingerprint]; got != originFP {
		t.Errorf("fingerprint = %q, want %q (escalation label must not enter it)", got, originFP)
	}
}

func TestNoEscalationLabelKeepsEscalatedOnlyAndNoRemediation(t *testing.T) {
	for _, esc := range []string{"", "none", "triager"} {
		opts := episodeOpts(t)
		spy := originSpy(esc)
		if err := tick(t, opts, spy, t0); err != nil {
			t.Fatal(err)
		}
		if len(spy.created) != 1 {
			t.Fatalf("escalation=%q: expected one create, got %+v", esc, spy.created)
		}
		if got := spy.created[0].labels; !slices.Equal(got, []string{"escalated"}) {
			t.Errorf("escalation=%q: labels = %v, want [escalated]", esc, got)
		}
		if strings.Contains(spy.created[0].body, "Remediation:") {
			t.Errorf("escalation=%q: default beads must not gain a Remediation: section:\n%s", esc, spy.created[0].body)
		}
	}
}

// labelFilteredList models pg-connector's `list --label escalated --status
// open,...` for the named dedup query: it returns only the previously
// CREATED beads that carry the "escalated" label, exactly as the real query
// would. A bead created without "escalated" (e.g. human-only) would be
// invisible to it.
func labelFilteredList(created *[]createdBead) func(string) []connectorIssue {
	return func(query string) []connectorIssue {
		if query != defaultDedupQuery {
			return nil
		}
		var out []connectorIssue
		for i, c := range *created {
			if slices.Contains(c.labels, "escalated") {
				out = append(out, connectorIssue{ID: "bead-" + string(rune('a'+i)), Labels: c.labels, Metadata: c.metadata})
			}
		}
		return out
	}
}

type createdBead struct {
	labels   []string
	metadata map[string]string
}

// The same escalation=human alert on two consecutive ticks files exactly ONE
// bead: "escalated" keeps the human bead visible to the dedup query.
func TestEscalationHumanDedupsAcrossConsecutiveTicks(t *testing.T) {
	opts := episodeOpts(t)
	var all []createdBead
	for i, at := range []time.Time{t0, t0.Add(30 * time.Minute)} {
		spy := originSpy("human")
		spy.listEscalatedByQuery = map[string][]connectorIssue{defaultDedupQuery: labelFilteredList(&all)(defaultDedupQuery)}
		if err := tick(t, opts, spy, at); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		for _, c := range spy.created {
			all = append(all, createdBead{labels: c.labels, metadata: c.metadata})
		}
	}
	if len(all) != 1 {
		t.Fatalf("two consecutive ticks must create exactly one bead, got %d: %+v", len(all), all)
	}
}

// Premise check: a human-ONLY bead would be invisible to the escalated-label
// dedup query, so the label filter really is what protects against a
// duplicate every tick.
func TestLabelFilteredListHidesHumanOnlyBead(t *testing.T) {
	created := []createdBead{{labels: []string{"human"}, metadata: map[string]string{metaFingerprint: originFP}}}
	if got := labelFilteredList(&created)(defaultDedupQuery); len(got) != 0 {
		t.Fatalf("human-only bead must be invisible to the escalated filter, got %+v", got)
	}
}

// Moving an already-registered rule to escalation=human must keep matching
// its open (escalated-only) bead rather than filing a second one.
func TestAddingEscalationHumanKeepsMatchingOpenBead(t *testing.T) {
	opts := episodeOpts(t)
	existing := connectorIssue{ID: "pg2-old", Labels: []string{"escalated"}, Metadata: map[string]string{
		metaFingerprint: originFP, metaState: "active", metaEpisodeCount: "1",
	}}
	seedAlertState(t, opts.snapshotPath, originFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0})
	spy := originSpy("human")
	spy.listEscalatedByQuery = map[string][]connectorIssue{defaultDedupQuery: {existing}}
	if err := tick(t, opts, spy, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("a duplicate bead was created after the rule gained escalation=human: %+v", spy.created)
	}
}

func TestEscalationLabelsHumanOnlyForGrafanaAlerts(t *testing.T) {
	if got := escalationLabels(finding{Kind: kindQueueGrowth, Escalation: escalationHuman}); !slices.Equal(got, []string{"escalated"}) {
		t.Fatalf("non-alert finding labels = %v", got)
	}
}

func TestRunCmdDocumentsEscalationLabel(t *testing.T) {
	cmd := newRunCmd()
	if !strings.Contains(cmd.Long, `escalation="human"`) || !strings.Contains(cmd.Long, "Remediation:") {
		t.Fatalf("run help must document the escalation label contract, got %q", cmd.Long)
	}
}
