package attention

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// stale-issue facts, as the connector's show payload stores them. The
// connector computes operator_updated_at from the operator's own comments and
// status transitions only; a comment or edit by anyone else never appears in
// it, so the desk-level model of "another user updated the issue" is a payload
// whose other-activity fields are newer while operator_updated_at is old.
type issueFactsSpec struct {
	state    string
	assignee string
	entered  string // status_changed_at
	operator string // operator_updated_at
	// other is a later update by someone else (the tracker's own "updated"
	// stamp); it MUST NOT affect the rule.
	other string
}

func (f issueFactsSpec) json() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, `"`+k+`":"`+v+`"`)
		}
	}
	add("id", "K-1")
	add("state", f.state)
	add("assignee", f.assignee)
	add("status_changed_at", f.entered)
	add("operator_updated_at", f.operator)
	add("updated_at", f.other)
	return `{"issue_show":{` + strings.Join(parts, ",") + `}}`
}

const (
	day   = 24 * time.Hour
	stamp = "2006-01-02T15:04:05Z"
)

// ago renders the RFC3339 time that is d before fixedNow.
func ago(d time.Duration) string { return fixedNow.Add(-d).Format(stamp) }

func evaluateIssueAt(t *testing.T, st *store.Store, cfg *config.Config, now time.Time) Result {
	t.Helper()
	res, err := Evaluate(Inputs{Store: st, Repo: testRepo, Config: cfg, Clock: interpret.FixedClock(now)})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

func issueItem(res Result, id string) (Item, bool) {
	for _, it := range res.Items {
		if it.Type == "issue" && it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

func TestIssueStaleInProgressRule(t *testing.T) {
	inProgress := "In Progress"
	tests := []struct {
		name        string
		facts       issueFactsSpec
		wantRaised  bool
		wantSummary string
	}{
		{
			name:  "6 days since the operator's update is not raised",
			facts: issueFactsSpec{state: inProgress, assignee: "me", entered: ago(30 * day), operator: ago(6 * day)},
		},
		{
			name:        "7 days since the operator's update is raised",
			facts:       issueFactsSpec{state: inProgress, assignee: "me", entered: ago(30 * day), operator: ago(7 * day)},
			wantRaised:  true,
			wantSummary: "In Progress with no update from me in 7 days",
		},
		{
			name:        "8 days and 23 hours rounds down to 8 days",
			facts:       issueFactsSpec{state: inProgress, assignee: "me", entered: ago(30 * day), operator: ago(8*day + 23*time.Hour)},
			wantRaised:  true,
			wantSummary: "In Progress with no update from me in 8 days",
		},
		{
			name:        "another user's update does not reset the clock",
			facts:       issueFactsSpec{state: inProgress, assignee: "me", entered: ago(30 * day), operator: ago(9 * day), other: ago(1 * day)},
			wantRaised:  true,
			wantSummary: "In Progress with no update from me in 9 days",
		},
		{
			name:  "an operator comment resets the clock",
			facts: issueFactsSpec{state: inProgress, assignee: "me", entered: ago(30 * day), operator: ago(1 * day)},
		},
		{
			name:  "an operator transition resets the clock",
			facts: issueFactsSpec{state: inProgress, assignee: "me", entered: ago(2 * day), operator: ago(2 * day)},
		},
		{
			name:        "no operator update at all is measured from the In Progress entry",
			facts:       issueFactsSpec{state: inProgress, assignee: "me", entered: ago(10 * day)},
			wantRaised:  true,
			wantSummary: "In Progress with no update from me in 10 days",
		},
		{
			name:  "no operator update, entered In Progress 3 days ago is not raised",
			facts: issueFactsSpec{state: inProgress, assignee: "me", entered: ago(3 * day)},
		},
		{
			name:  "an issue another person moved into In Progress recently is measured from the entry",
			facts: issueFactsSpec{state: inProgress, assignee: "me", entered: ago(2 * day), operator: ago(20 * day)},
		},
		{
			name:  "not In Progress never raises",
			facts: issueFactsSpec{state: "To Do", assignee: "me", entered: ago(60 * day), operator: ago(60 * day)},
		},
		{
			name:  "unassigned never raises",
			facts: issueFactsSpec{state: inProgress, entered: ago(60 * day), operator: ago(60 * day)},
		},
		{
			name:  "assigned to someone else carries no operator facts and never raises",
			facts: issueFactsSpec{state: inProgress, assignee: "someone-else", other: ago(60 * day)},
		},
		{
			name:  "unknown operator facts never raise",
			facts: issueFactsSpec{state: inProgress, assignee: "me"},
		},
		{
			name:        "the configured In Progress status name is matched case-insensitively",
			facts:       issueFactsSpec{state: "in progress", assignee: "me", entered: ago(30 * day), operator: ago(12 * day)},
			wantRaised:  true,
			wantSummary: "In Progress with no update from me in 12 days",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			putIssue(t, st, "K-1", tc.facts.json())
			res := evaluateIssueAt(t, st, baseConfig(), fixedNow)

			it, raised := issueItem(res, "K-1")
			if raised != tc.wantRaised {
				t.Fatalf("raised = %v, want %v (items %+v, trace %+v)", raised, tc.wantRaised, res.Items, res.Traces[Ref("issue", "K-1")])
			}
			if !raised {
				return
			}
			if it.Rule != KindIssueStaleInProgress || it.Summary != tc.wantSummary || it.Severity != SeverityMedium {
				t.Errorf("item = %+v, want rule %s, summary %q, severity medium", it, KindIssueStaleInProgress, tc.wantSummary)
			}
			// The item's {type, id} is the issue's own ref, which
			// `pg-desk links` and the menu bar resolve to the issue's page.
			if it.Type != "issue" || it.ID != "K-1" || it.Group != "issue:K-1" {
				t.Errorf("item ref = %s:%s group %q, want issue:K-1", it.Type, it.ID, it.Group)
			}
		})
	}
}

// The clock is an input: the same stored facts raise or clear as the injected
// clock advances, with no event on the entity.
func TestIssueStaleInProgressInjectedClock(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", issueFactsSpec{state: "In Progress", assignee: "me", entered: ago(30 * day), operator: ago(6 * day)}.json())

	if _, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow), "K-1"); ok {
		t.Error("6 days old at fixedNow must not raise")
	}
	if it, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow.Add(1*day)), "K-1"); !ok || !strings.Contains(it.Summary, "7 days") {
		t.Errorf("one day later the same facts must raise at 7 days, got %+v ok=%v", it, ok)
	}
	if _, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow.Add(-5*day)), "K-1"); ok {
		t.Error("a clock before the update (skew) must not raise")
	}
}

// The item clears on the next evaluation after the operator updates the
// issue, after it leaves In Progress, and after it is reassigned (the
// connector then stops supplying operator facts).
func TestIssueStaleInProgressClears(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	stale := issueFactsSpec{state: "In Progress", assignee: "me", entered: ago(30 * day), operator: ago(9 * day)}
	putIssue(t, st, "K-1", stale.json())
	if _, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow), "K-1"); !ok {
		t.Fatal("setup: the stale issue must raise")
	}

	for name, next := range map[string]issueFactsSpec{
		"operator updated":    {state: "In Progress", assignee: "me", entered: ago(30 * day), operator: ago(1 * time.Hour)},
		"left In Progress":    {state: "Done", assignee: "me", entered: ago(1 * day), operator: ago(1 * day)},
		"reassigned":          {state: "In Progress", assignee: "someone-else"},
		"operator facts lost": {state: "In Progress", assignee: "me"},
	} {
		t.Run(name, func(t *testing.T) {
			putIssue(t, st, "K-1", next.json())
			if it, ok := issueItem(evaluateIssueAt(t, st, baseConfig(), fixedNow), "K-1"); ok {
				t.Errorf("the item must clear on the next evaluation, still raised: %+v", it)
			}
			putIssue(t, st, "K-1", stale.json())
		})
	}
}

func TestIssueStaleInProgressConfiguration(t *testing.T) {
	stale := func(age time.Duration) string {
		return issueFactsSpec{state: "In Progress", assignee: "me", entered: ago(60 * day), operator: ago(age)}.json()
	}
	days := func(n int) *int { return &n }
	no := false

	t.Run("threshold is configurable", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", stale(5*day))

		cfg := baseConfig()
		if _, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); ok {
			t.Error("5 days is under the 7 day default")
		}
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueStaleInProgress: {StaleAfterDays: days(3)}}
		if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); !ok || it.Summary != "In Progress with no update from me in 5 days" {
			t.Errorf("a 3 day threshold must raise at 5 days, got %+v ok=%v", it, ok)
		}
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueStaleInProgress: {StaleAfterDays: days(10)}}
		putIssue(t, st, "K-1", stale(9*day))
		if _, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); ok {
			t.Error("a 10 day threshold must not raise at 9 days")
		}
	})

	t.Run("severity is configurable", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", stale(8*day))
		cfg := baseConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueStaleInProgress: {Severity: "high"}}
		if it, ok := issueItem(evaluateIssueAt(t, st, cfg, fixedNow), "K-1"); !ok || it.Severity != SeverityHigh {
			t.Errorf("item = %+v ok=%v, want severity high", it, ok)
		}
	})

	t.Run("the rule can be disabled", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", stale(8*day))
		cfg := baseConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{KindIssueStaleInProgress: {Enabled: &no}}
		res := evaluateIssueAt(t, st, cfg, fixedNow)
		if _, ok := issueItem(res, "K-1"); ok {
			t.Error("a disabled rule must not raise")
		}
		if why := res.Traces[Ref("issue", "K-1")].NotRaised[KindIssueStaleInProgress]; why != "disabled" {
			t.Errorf("explain reason = %q, want disabled", why)
		}
	})

	t.Run("suppress annotation drops it with a recorded reason", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		putIssue(t, st, "K-1", stale(8*day))
		if err := st.SetAnnotation(store.KVAnnotation{Repo: testRepo, EntityType: "issue", EntityID: "K-1", Key: store.KeySuppress(KindIssueStaleInProgress), Value: "true", Origin: "test", SetBy: "test", SetAt: "2026-10-06T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		res := evaluateIssueAt(t, st, baseConfig(), fixedNow)
		if _, ok := issueItem(res, "K-1"); ok {
			t.Error("a suppressed candidate must not surface")
		}
		if d := res.Traces[Ref("issue", "K-1")].Dropped; len(d) != 1 || d[0].By != store.KeySuppress(KindIssueStaleInProgress) {
			t.Errorf("dropped = %+v, want the suppressor recorded", d)
		}
	})
}

func TestResolveStaleAfter(t *testing.T) {
	days := func(n int) *int { return &n }
	s, err := Resolve(config.AttentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Rules[KindIssueStaleInProgress].StaleAfter; got != 7*day {
		t.Errorf("default threshold = %v, want 7 days", got)
	}
	if got := s.Rules[KindOwnCIFailing].StaleAfter; got != 0 {
		t.Errorf("a rule with no such parameter has threshold %v, want 0", got)
	}
	s, err = Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{KindIssueStaleInProgress: {StaleAfterDays: days(14)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Rules[KindIssueStaleInProgress].StaleAfter; got != 14*day {
		t.Errorf("configured threshold = %v, want 14 days", got)
	}

	_, err = Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{KindOwnCIFailing: {StaleAfterDays: days(3)}}})
	if err == nil || !strings.Contains(err.Error(), "attention.rules.pr.own-ci-failing.stale_after_days") || !strings.Contains(err.Error(), KindIssueStaleInProgress) {
		t.Errorf("stale_after_days on a rule without it must be rejected, naming the key and the rules that have it; got %v", err)
	}
}
