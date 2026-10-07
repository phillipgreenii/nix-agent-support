package attention

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// dueFacts is a stored issue payload with the fields the due-date rules read.
// The assignee is deliberately absent from the default: the rules do not look
// at it, the deployment's watch queries decide whose issues are watched.
func dueFacts(state, due string) string {
	parts := []string{`"id":"K-1"`}
	if state != "" {
		parts = append(parts, `"state":"`+state+`"`)
	}
	if due != "" {
		parts = append(parts, `"due_date":"`+due+`"`)
	}
	return `{"issue_show":{` + strings.Join(parts, ",") + `}}`
}

// fixedNow is 2026-10-06 12:00 UTC.
func TestIssueDueRules(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		due         string
		wantRule    string // "" = nothing raised
		wantSummary string
		wantSev     Severity
	}{
		// bare dates (Jira's duedate): due at the END of that day
		{name: "date due today is due soon, not overdue, until the day ends", state: "In Progress", due: "2026-10-06", wantRule: KindIssueDueSoon, wantSummary: "Due in under a day", wantSev: SeverityMedium},
		{name: "date due tomorrow is due soon", state: "To Do", due: "2026-10-07", wantRule: KindIssueDueSoon, wantSummary: "Due in 1 day", wantSev: SeverityMedium},
		{name: "date due the day after tomorrow is beyond the default window (2.5 days left)", state: "To Do", due: "2026-10-08"},
		{name: "date due in 3 days is beyond the default window", state: "To Do", due: "2026-10-09"},
		{name: "date due yesterday is overdue", state: "To Do", due: "2026-10-05", wantRule: KindIssueOverdue, wantSummary: "Overdue by under a day", wantSev: SeverityHigh},
		{name: "date due 3 days ago is overdue by 2 whole days", state: "To Do", due: "2026-10-03", wantRule: KindIssueOverdue, wantSummary: "Overdue by 2 days", wantSev: SeverityHigh},
		// instants (beads' due_at)
		{name: "instant 3 hours ahead is due soon", state: "open", due: "2026-10-06T15:00:00Z", wantRule: KindIssueDueSoon, wantSummary: "Due in under a day", wantSev: SeverityMedium},
		{name: "instant 1 hour ago is overdue", state: "open", due: "2026-10-06T11:00:00Z", wantRule: KindIssueOverdue, wantSummary: "Overdue by under a day", wantSev: SeverityHigh},
		{name: "instant exactly now is overdue, not due soon", state: "open", due: "2026-10-06T12:00:00Z", wantRule: KindIssueOverdue, wantSummary: "Overdue by under a day", wantSev: SeverityHigh},
		{name: "instant 5 days ahead raises nothing", state: "open", due: "2026-10-11T12:00:00Z"},
		{name: "instant with an offset is compared as an instant", state: "open", due: "2026-10-06T07:00:00-04:00", wantRule: KindIssueOverdue, wantSummary: "Overdue by under a day", wantSev: SeverityHigh},
		// not raising
		{name: "no due date raises nothing", state: "To Do"},
		{name: "unreadable due date raises nothing", state: "To Do", due: "next tuesday"},
		{name: "done status never raises, even long overdue", state: "Done", due: "2026-09-01"},
		{name: "closed beads status never raises", state: "closed", due: "2026-09-01T00:00:00Z"},
		{name: "done matching is case-insensitive", state: "DONE", due: "2026-09-01"},
		{name: "unknown status raises nothing", state: "", due: "2026-09-01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			putIssue(t, st, "K-1", dueFacts(tc.state, tc.due))
			res := evaluateIssueAt(t, st, baseConfig(), fixedNow)
			it, raised := issueItem(res, "K-1")
			if tc.wantRule == "" {
				if raised {
					t.Fatalf("raised %+v, want nothing", it)
				}
				return
			}
			if !raised {
				t.Fatalf("nothing raised, want %s (trace %+v)", tc.wantRule, res.Traces[Ref("issue", "K-1")])
			}
			if it.Rule != tc.wantRule || it.Summary != tc.wantSummary || it.Severity != tc.wantSev {
				t.Errorf("item = %+v, want rule %s, summary %q, severity %s", it, tc.wantRule, tc.wantSummary, tc.wantSev)
			}
			if it.Type != "issue" || it.ID != "K-1" || it.Group != "issue:K-1" {
				t.Errorf("item ref = %s:%s group %q, want issue:K-1", it.Type, it.ID, it.Group)
			}
		})
	}
}

// The two rules are mutually exclusive for every moment: sweeping the clock
// across the due instant never yields both and never skips straight past it.
func TestIssueDueRulesAreMutuallyExclusive(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", dueFacts("open", "2026-10-06T12:00:00Z"))
	var seq []string
	for _, off := range []time.Duration{-72 * time.Hour, -49 * time.Hour, -47 * time.Hour, -time.Minute, 0, time.Minute, 30 * time.Hour} {
		res := evaluateIssueAt(t, st, baseConfig(), fixedNow.Add(off))
		it, ok := issueItem(res, "K-1")
		if !ok {
			seq = append(seq, "-")
			continue
		}
		seq = append(seq, it.Rule)
	}
	want := []string{"-", "-", KindIssueDueSoon, KindIssueDueSoon, KindIssueOverdue, KindIssueOverdue, KindIssueOverdue}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Errorf("sequence = %v, want %v", seq, want)
	}
}

// The clock is an input: a date-only due day ends in the clock's zone.
func TestIssueDueDateOnlyUsesTheClockZone(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-06"))
	la := time.FixedZone("UTC-7", -7*3600)
	// 2026-10-07 02:00 UTC is 2026-10-06 19:00 in UTC-7: still the due day there.
	inZone := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC).In(la)
	if it, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), inZone), "K-1"); !ok || it.Rule != KindIssueDueSoon {
		t.Errorf("19:00 on the due day in the clock's zone must be due soon, got %+v ok=%v", it, ok)
	}
	// The same instant read in UTC is already the next day.
	if it, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), inZone.UTC()), "K-1"); !ok || it.Rule != KindIssueOverdue {
		t.Errorf("02:00 the next day in UTC must be overdue, got %+v ok=%v", it, ok)
	}
	// Result.Now stays UTC whatever the clock's zone.
	if res := evaluateIssueAt(t, st, baseConfig(), inZone); res.Now.Location() != time.UTC {
		t.Errorf("Result.Now zone = %v, want UTC", res.Now.Location())
	}
}

func TestIssueDueClears(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-01"))
	if _, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow), "K-1"); !ok {
		t.Fatal("setup: the overdue issue must raise")
	}
	for name, next := range map[string]string{
		"moved to done":      dueFacts("Done", "2026-10-01"),
		"due date extended":  dueFacts("To Do", "2026-11-01"),
		"due date removed":   dueFacts("To Do", ""),
		"closed in beads":    dueFacts("closed", "2026-10-01"),
		"configured as done": dueFacts("Shipped", "2026-10-01"),
	} {
		t.Run(name, func(t *testing.T) {
			putIssue(t, st, "K-1", next)
			cfg := baseConfig()
			if name == "configured as done" {
				cfg.Jira = &config.JiraConfig{DoneStatuses: []string{"Shipped"}}
			}
			if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); ok {
				t.Errorf("the item must clear on the next evaluation, still raised: %+v", it)
			}
			putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-01"))
		})
	}
}

func TestIssueDueConfiguration(t *testing.T) {
	days := func(n int) *int { return &n }
	no := false

	t.Run("window is configurable", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-09")) // end of Oct 9 is 3.5 days away
		if _, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow), "K-1"); ok {
			t.Error("3 days is beyond the 2 day default")
		}
		cfg := baseConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueDueSoon: {DueSoonDays: days(5)}}
		if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); !ok || it.Rule != KindIssueDueSoon || it.Summary != "Due in 3 days" {
			t.Errorf("a 5 day window must raise at 3 days, got %+v ok=%v", it, ok)
		}
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueDueSoon: {DueSoonDays: days(1)}}
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-08"))
		if _, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); ok {
			t.Error("a 1 day window must not raise at 2 days")
		}
	})

	t.Run("severity is configurable", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-01"))
		cfg := baseConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueOverdue: {Severity: "low"}}
		if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); !ok || it.Severity != SeverityLow {
			t.Errorf("item = %+v ok=%v, want severity low", it, ok)
		}
	})

	t.Run("each rule can be disabled independently", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-01"))
		cfg := baseConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueOverdue: {Enabled: &no}}
		res := evaluateIssueAt(t, st, cfg, fixedNow)
		if _, ok := issueItem(res, "K-1"); ok {
			t.Error("a disabled rule must not raise")
		}
		if why := res.Traces[Ref("issue", "K-1")].NotRaised[KindIssueOverdue]; why != "disabled" {
			t.Errorf("explain reason = %q, want disabled", why)
		}
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-07"))
		if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); !ok || it.Rule != KindIssueDueSoon {
			t.Errorf("due-soon must still raise with overdue disabled, got %+v ok=%v", it, ok)
		}
	})

	t.Run("suppress annotation drops it with a recorded reason", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", dueFacts("To Do", "2026-10-01"))
		if err := st.SetAnnotation(store.KVAnnotation{Repo: testRepo, EntityType: "issue", EntityID: "K-1", Key: store.KeySuppress(KindIssueOverdue), Value: "true", Origin: "test", SetBy: "test", SetAt: "2026-10-06T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		res := evaluateIssueAt(t, st, baseConfig(), fixedNow)
		if _, ok := issueItem(res, "K-1"); ok {
			t.Error("a suppressed candidate must not surface")
		}
		if d := res.Traces[Ref("issue", "K-1")].Dropped; len(d) != 1 || d[0].By != store.KeySuppress(KindIssueOverdue) {
			t.Errorf("dropped = %+v, want the suppressor recorded", d)
		}
	})
}

// One item per entity: an overdue issue that is also stale in progress keeps
// the most severe candidate (overdue, high) and counts the other.
func TestIssueDueCollapsesWithStaleInProgress(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", `{"issue_show":{"id":"K-1","state":"In Progress","assignee":"me","due_date":"2026-10-01","status_changed_at":"`+ago(30*day)+`","operator_updated_at":"`+ago(20*day)+`"}}`)
	res := evaluateIssueAt(t, st, baseConfig(), fixedNow)
	n := 0
	for _, it := range res.Items {
		if it.Type == "issue" && it.ID == "K-1" {
			n++
			if it.Rule != KindIssueOverdue || it.Severity != SeverityHigh {
				t.Errorf("primary item = %+v, want issue.overdue at high", it)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d items for one issue, want exactly 1 (INV-ATTNEVAL-3)", n)
	}
}

func TestResolveDueSoon(t *testing.T) {
	days := func(n int) *int { return &n }
	s, err := Resolve(config.AttentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Rules[KindIssueDueSoon].DueSoon; got != 2*day {
		t.Errorf("default window = %v, want 2 days", got)
	}
	if got := s.Rules[KindIssueOverdue].DueSoon; got != 0 {
		t.Errorf("a rule with no such parameter has window %v, want 0", got)
	}
	s, err = Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{KindIssueDueSoon: {DueSoonDays: days(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Rules[KindIssueDueSoon].DueSoon; got != 7*day {
		t.Errorf("configured window = %v, want 7 days", got)
	}
	for _, kind := range []string{KindIssueOverdue, KindIssueStaleInProgress, KindOwnCIFailing} {
		_, err = Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{kind: {DueSoonDays: days(3)}}})
		if err == nil || !strings.Contains(err.Error(), "attention.rules."+kind+".due_soon_days") || !strings.Contains(err.Error(), KindIssueDueSoon) {
			t.Errorf("due_soon_days on %s must be rejected, naming the key and the rule that has it; got %v", kind, err)
		}
	}
}
