package interpret

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// --- ownership: ported verbatim from packages/pg-pr/internal/ownership/ownership_test.go ---

func TestClassifyOwnership(t *testing.T) {
	tests := []struct {
		name          string
		self          string
		prAuthor      string
		commitAuthors []string
		want          Ownership
	}{
		{"authored-by-me => mine", "me", "me", nil, OwnershipMine},
		{"mine wins even with others' commits", "me", "me", []string{"you"}, OwnershipMine},
		{"teammate + my commit => co-owned", "me", "you", []string{"you", "me"}, OwnershipCoOwned},
		{"teammate + no commit of mine => team", "me", "you", []string{"you"}, OwnershipTeam},
		{"empty self => team", "", "me", []string{"me"}, OwnershipTeam},
		{"nil commits (degrade) teammate => team", "me", "you", nil, OwnershipTeam},
		{"nil commits (degrade) mine => mine", "me", "me", nil, OwnershipMine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOwnership(tt.self, tt.prAuthor, tt.commitAuthors); got != tt.want {
				t.Errorf("classifyOwnership(%q,%q,%v) = %q, want %q", tt.self, tt.prAuthor, tt.commitAuthors, got, tt.want)
			}
		})
	}
}

func TestOwnershipActsAsMine(t *testing.T) {
	if !OwnershipMine.ActsAsMine() || !OwnershipCoOwned.ActsAsMine() || OwnershipTeam.ActsAsMine() {
		t.Errorf("ActsAsMine: Mine=%v CoOwned=%v Team=%v; want true true false",
			OwnershipMine.ActsAsMine(), OwnershipCoOwned.ActsAsMine(), OwnershipTeam.ActsAsMine())
	}
}

// --- enrichment: ported verbatim from packages/pg-pr/internal/enrich/enrich_test.go ---

func TestBucketSize(t *testing.T) {
	cases := []struct {
		total int
		want  string
	}{
		{0, "XS"},
		{9, "XS"},
		{10, "S"},
		{29, "S"},
		{30, "M"},
		{99, "M"},
		{100, "L"},
		{499, "L"},
		{500, "XL"},
		{5000, "XL"},
	}
	for _, c := range cases {
		if got := bucketSize(c.total); got != c.want {
			t.Errorf("bucketSize(%d) = %q; want %q", c.total, got, c.want)
		}
	}
}

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		branch  string
		commits []string
		want    string
	}{
		{"title conventional fix", "fix(store): wrong scan", "anything", nil, "bugfix"},
		{"title feat with bang", "feat!: breaking change", "x", nil, "feature"},
		{"branch prefix when title plain", "tidy things up", "refactor/cleanup", nil, "refactor"},
		{"branch feature alias", "stuff", "feature/new-ui", nil, "feature"},
		{"commit majority when title+branch plain", "wip", "wip", []string{"fix: a", "fix: b", "docs: c"}, "bugfix"},
		{"fallback other", "random work", "wip", nil, "other"},
		{"title wins over branch", "docs: readme", "fix/typo", nil, "docs"},
	}
	for _, c := range cases {
		if got := classifyKind(c.title, c.branch, c.commits); got != c.want {
			t.Errorf("%s: classifyKind(%q,%q,%v) = %q; want %q", c.name, c.title, c.branch, c.commits, got, c.want)
		}
	}
}

// --- base urgency: adapted from enrich_test.go's TestScoreUrgency; ---
// --- documented deviation: prShow/ciRollupResult replace api.PR/api.CIRun. ---

func TestScoreUrgency(t *testing.T) {
	t.Run("none -> low", func(t *testing.T) {
		score, reasons := scoreUrgency(prShow{Title: "feat: x", Body: "normal"}, nil, ciRollupResult{State: "success"}, nil)
		if score != 0 || len(reasons) != 0 {
			t.Fatalf("got score=%d reasons=%v; want 0/[]", score, reasons)
		}
	})
	t.Run("urgency label -> high", func(t *testing.T) {
		score, reasons := scoreUrgency(prShow{Title: "x", Labels: []string{"P0"}}, nil, ciRollupResult{}, nil)
		if score != 3 || !reflect.DeepEqual(reasons, []string{"label:p0"}) {
			t.Fatalf("got score=%d reasons=%v; want 3/[label:p0]", score, reasons)
		}
		if levelForScore(score, defaultUrgencyThresholds) != "high" {
			t.Fatalf("levelForScore(%d) = %q; want high", score, levelForScore(score, defaultUrgencyThresholds))
		}
	})
	t.Run("keyword -> medium", func(t *testing.T) {
		score, reasons := scoreUrgency(prShow{Title: "Fix for production incident"}, nil, ciRollupResult{}, nil)
		if !reflect.DeepEqual(reasons, []string{"keyword:production incident"}) {
			t.Fatalf("got reasons=%v; want [keyword:production incident]", reasons)
		}
		if levelForScore(score, defaultUrgencyThresholds) != "medium" {
			t.Fatalf("levelForScore(%d) = %q; want medium", score, levelForScore(score, defaultUrgencyThresholds))
		}
	})
	t.Run("bugfix commit alone -> medium", func(t *testing.T) {
		score, _ := scoreUrgency(prShow{Title: "wip"}, []prCommit{{Message: "fix: a"}}, ciRollupResult{}, nil)
		if score != 1 || levelForScore(score, defaultUrgencyThresholds) != "medium" {
			t.Fatalf("got score=%d; want 1/medium", score)
		}
	})
	t.Run("ci failing -> medium", func(t *testing.T) {
		score, _ := scoreUrgency(prShow{Title: "x"}, nil, ciRollupResult{State: "failure"}, nil)
		if score != 2 || levelForScore(score, defaultUrgencyThresholds) != "medium" {
			t.Fatalf("got score=%d; want 2/medium", score)
		}
	})
	t.Run("keyword + ci failing -> high", func(t *testing.T) {
		score, _ := scoreUrgency(prShow{Title: "hotfix outage"}, nil, ciRollupResult{State: "failure"}, nil)
		if score < 3 || levelForScore(score, defaultUrgencyThresholds) != "high" {
			t.Fatalf("got score=%d; want >=3/high", score)
		}
	})
	t.Run("excluded ci check does not count as failing", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"runs": []map[string]any{{"name": "policy-bot: x", "status": "completed", "conclusion": "failure"}},
		})
		ci := computeCIRollup(raw, []config.CheckInterpreterConfig{{Patterns: []string{"^policy-bot"}}})
		score, reasons := scoreUrgency(prShow{Title: "x"}, nil, ci, nil)
		if score != 0 || len(reasons) != 0 {
			t.Fatalf("got score=%d reasons=%v; want 0/[] (excluded check must not count)", score, reasons)
		}
	})
	t.Run("config-driven label overrides default vocabulary", func(t *testing.T) {
		cfg := &config.UrgencyConfig{Labels: []string{"my-custom-urgent"}}
		score, reasons := scoreUrgency(prShow{Title: "x", Labels: []string{"p0"}}, nil, ciRollupResult{}, cfg)
		if score != 0 || len(reasons) != 0 {
			t.Fatalf("default label p0 must not fire once config supplies its own list: score=%d reasons=%v", score, reasons)
		}
		score, reasons = scoreUrgency(prShow{Title: "x", Labels: []string{"my-custom-urgent"}}, nil, ciRollupResult{}, cfg)
		if score != 3 || !reflect.DeepEqual(reasons, []string{"label:my-custom-urgent"}) {
			t.Fatalf("configured label did not fire: score=%d reasons=%v", score, reasons)
		}
	})
}

// --- layered urgency: Jira half (docket pg2-2j5ac.40, Phase 13) -----------

// jiraIssueRaw marshals a jiraIssueFields literal to json.RawMessage, this
// test file's own shorthand for building a gather.Facts.JiraIssues-shaped
// map without going through a real gather call.
func jiraIssueRaw(t *testing.T, f jiraIssueFields) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal jiraIssueFields: %v", err)
	}
	return raw
}

// TestScoreUrgencyWithHealth_HighPriorityXref_HigherLevelThanNoXref is this
// packet's own pinned acceptance criterion: "scoreUrgencyWithHealth
// returns a higher level for a PR xref'd to an issue whose priority is in
// config.Jira.HighPriorityValues than for an otherwise-identical PR with
// no such xref."
func TestScoreUrgencyWithHealth_HighPriorityXref_HigherLevelThanNoXref(t *testing.T) {
	pr := prShow{Title: "x"}
	ci := ciRollupResult{State: "success"}
	jiraCfg := &config.JiraConfig{HighPriorityValues: []string{"Highest"}}

	withoutXref := scoreUrgencyWithHealth(pr, nil, ci, nil, nil, jiraCfg)
	withXref := scoreUrgencyWithHealth(pr, nil, ci, nil,
		[]jiraIssueFields{{ID: "PROJ-1", Priority: "Highest"}}, jiraCfg)

	if withXref.Score <= withoutXref.Score {
		t.Fatalf("withXref.Score=%d, want strictly greater than withoutXref.Score=%d", withXref.Score, withoutXref.Score)
	}
	if levelRank(withXref.Level) <= levelRank(withoutXref.Level) {
		t.Fatalf("withXref.Level=%q, want strictly higher than withoutXref.Level=%q", withXref.Level, withoutXref.Level)
	}
}

// levelRank orders low < medium < high, for the strict-inequality
// assertion above.
func levelRank(level string) int {
	switch level {
	case "high":
		return 2
	case "medium":
		return 1
	default:
		return 0
	}
}

// TestScoreUrgencyWithHealth_DegradesToBase proves the Binding decisions'
// "MUST degrade to the base computeUrgency/scoreUrgency signal" rule, both
// halves: a nil config.Jira, and a PR with no cross-referenced Jira issue
// at all, each return computeUrgency's own result unchanged.
func TestScoreUrgencyWithHealth_DegradesToBase(t *testing.T) {
	pr := prShow{Title: "hotfix outage"} // fires the base keyword signal, so base itself is non-zero
	ci := ciRollupResult{State: "success"}
	base := computeUrgency(pr, nil, ci, nil)

	t.Run("nil jira config", func(t *testing.T) {
		got := scoreUrgencyWithHealth(pr, nil, ci, nil,
			[]jiraIssueFields{{ID: "PROJ-1", Priority: "Highest"}}, nil)
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("scoreUrgencyWithHealth(nil jiraCfg) = %+v, want computeUrgency's own %+v unchanged", got, base)
		}
	})

	t.Run("no cross-referenced jira issue", func(t *testing.T) {
		jiraCfg := &config.JiraConfig{HighPriorityValues: []string{"Highest"}}
		got := scoreUrgencyWithHealth(pr, nil, ci, nil, nil, jiraCfg)
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("scoreUrgencyWithHealth(no jiraIssues) = %+v, want computeUrgency's own %+v unchanged", got, base)
		}
	})

	t.Run("cross-referenced issue matches no jira criterion", func(t *testing.T) {
		jiraCfg := &config.JiraConfig{HighPriorityValues: []string{"Highest"}, IncidentLabels: []string{"incident"}, IncidentIssueTypes: []string{"Incident"}}
		got := scoreUrgencyWithHealth(pr, nil, ci, nil,
			[]jiraIssueFields{{ID: "PROJ-1", Priority: "Low", Labels: []string{"enhancement"}, IssueType: "Story"}}, jiraCfg)
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("scoreUrgencyWithHealth(non-matching jiraIssue) = %+v, want computeUrgency's own %+v unchanged", got, base)
		}
	})
}

// TestScoreUrgencyWithHealth_IncidentLabelAndIssueType proves the OTHER
// two Jira signal sources jiraIssueSignal recognizes (config.Jira's
// incident_labels and incident_issue_types), not only high_priority_values.
func TestScoreUrgencyWithHealth_IncidentLabelAndIssueType(t *testing.T) {
	pr := prShow{Title: "x"}
	ci := ciRollupResult{State: "success"}
	base := computeUrgency(pr, nil, ci, nil)

	t.Run("incident label", func(t *testing.T) {
		jiraCfg := &config.JiraConfig{IncidentLabels: []string{"incident"}}
		got := scoreUrgencyWithHealth(pr, nil, ci, nil,
			[]jiraIssueFields{{ID: "PROJ-1", Labels: []string{"incident"}}}, jiraCfg)
		if got.Score <= base.Score {
			t.Fatalf("incident-labeled xref did not raise the score: got=%d base=%d", got.Score, base.Score)
		}
	})

	t.Run("incident issue type", func(t *testing.T) {
		jiraCfg := &config.JiraConfig{IncidentIssueTypes: []string{"Incident"}}
		got := scoreUrgencyWithHealth(pr, nil, ci, nil,
			[]jiraIssueFields{{ID: "PROJ-1", IssueType: "Incident"}}, jiraCfg)
		if got.Score <= base.Score {
			t.Fatalf("incident issue_type xref did not raise the score: got=%d base=%d", got.Score, base.Score)
		}
	})
}

// TestDecodeJiraIssues proves the decode helper: a well-formed map decodes
// every entry in sorted-by-key order (deterministic, not map-iteration-order
// dependent), and a malformed entry is skipped rather than erroring the
// whole call.
func TestDecodeJiraIssues(t *testing.T) {
	if got := decodeJiraIssues(nil); got != nil {
		t.Fatalf("decodeJiraIssues(nil) = %v, want nil", got)
	}

	raw := map[string]json.RawMessage{
		"PROJ-2": jiraIssueRaw(t, jiraIssueFields{ID: "PROJ-2", Priority: "Low"}),
		"PROJ-1": jiraIssueRaw(t, jiraIssueFields{ID: "PROJ-1", Priority: "Highest"}),
		"BAD-1":  json.RawMessage(`{not valid json`),
	}
	got := decodeJiraIssues(raw)
	if len(got) != 2 {
		t.Fatalf("decodeJiraIssues returned %d entries, want 2 (malformed entry skipped): %+v", len(got), got)
	}
	if got[0].ID != "PROJ-1" || got[1].ID != "PROJ-2" {
		t.Fatalf("decodeJiraIssues order = [%s, %s], want sorted-by-key [PROJ-1, PROJ-2]", got[0].ID, got[1].ID)
	}
}

// --- CI rollup ---

func TestComputeCIRollup(t *testing.T) {
	mk := func(runs ...map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"runs": runs})
		return b
	}
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"no runs", mk(), "none"},
		{"one passing", mk(map[string]any{"status": "completed", "conclusion": "success"}), "success"},
		{"one failing", mk(map[string]any{"status": "completed", "conclusion": "failure"}), "failure"},
		{"one pending", mk(map[string]any{"status": "in_progress"}), "pending"},
		{"failure wins over pending", mk(
			map[string]any{"status": "completed", "conclusion": "failure"},
			map[string]any{"status": "in_progress"},
		), "failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeCIRollup(tt.raw, nil).State; got != tt.want {
				t.Errorf("computeCIRollup(%s) = %q; want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// --- languages: new (documented deviation from go-enry) ---

func TestDetectLanguages(t *testing.T) {
	got := detectLanguages([]string{"a.go", "b.go", "c.py", "d.unknownext"})
	want := []string{"Go", "Python"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectLanguages = %v; want %v", got, want)
	}
	if got := detectLanguages(nil); got != nil {
		t.Fatalf("detectLanguages(nil) = %v; want nil", got)
	}
}

// --- category: new, config-vocabulary-driven ---

func TestClassifyCategory(t *testing.T) {
	vocab := map[string][]string{
		"bug":     {"fix", "bug"},
		"feature": {"add", "feature"},
	}
	if got := classifyCategory(prShow{Title: "fix: fix bug in bug tracker"}, vocab); got != "bug" {
		t.Errorf("classifyCategory = %q; want bug", got)
	}
	if got := classifyCategory(prShow{Title: "add a feature"}, vocab); got != "feature" {
		t.Errorf("classifyCategory = %q; want feature", got)
	}
	if got := classifyCategory(prShow{Title: "nothing matches"}, vocab); got != "" {
		t.Errorf("classifyCategory = %q; want \"\"", got)
	}
	if got := classifyCategory(prShow{Title: "anything"}, nil); got != "" {
		t.Errorf("classifyCategory with empty vocabulary = %q; want \"\"", got)
	}
}

// --- match reasons ---

func TestComputeMatchReasons(t *testing.T) {
	pr := prShow{
		Author:         "teammate",
		ReviewRequests: []string{"me"},
		Reviews:        []prReview{{Author: "me", State: "COMMENTED"}},
		Labels:         []string{"lbl-one", "other"},
	}
	got := computeMatchReasons(pr, []string{"teammate"}, []string{"lbl-one"}, "me")
	want := []string{MatchReasonTeamAuthored, MatchReasonReviewRequested, MatchReasonReviewedByMe, "label:lbl-one"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("computeMatchReasons = %v; want %v", got, want)
	}
}

// --- approvals / bot verdict ---

func TestComputeApprovals(t *testing.T) {
	pr := prShow{Reviews: []prReview{
		{Author: "alice", State: "APPROVED"},
		{Author: "bob", State: "APPROVED"},
		{Author: "policy-bot", State: "CHANGES_REQUESTED"},
	}}
	appr := computeApprovals(pr, []string{"policy-bot"})
	if appr.HumanApprovers != 2 || !appr.HumanApproved {
		t.Fatalf("got HumanApprovers=%d HumanApproved=%v; want 2/true", appr.HumanApprovers, appr.HumanApproved)
	}
	if appr.BotVerdict != BotVerdictDisapproved {
		t.Fatalf("got BotVerdict=%q; want disapproved", appr.BotVerdict)
	}

	prApproved := prShow{Reviews: []prReview{{Author: "policy-bot", State: "APPROVED"}}}
	if got := computeApprovals(prApproved, []string{"policy-bot"}).BotVerdict; got != BotVerdictApproved {
		t.Fatalf("got BotVerdict=%q; want approved", got)
	}

	prNoDecision := prShow{Reviews: []prReview{{Author: "policy-bot", State: "COMMENTED"}}}
	if got := computeApprovals(prNoDecision, []string{"policy-bot"}).BotVerdict; got != BotVerdictNoDecision {
		t.Fatalf("got BotVerdict=%q; want no-decision", got)
	}
}

// --- waiting-on-me: ported from pkg/beads.AllNonClosedHumanLabeled's own semantics ---

func TestComputeWaitingOnMe(t *testing.T) {
	mk := func(entities ...map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"entities": entities})
		return b
	}
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{"empty", nil, false},
		{"no entities", mk(), false},
		{"all closed", mk(map[string]any{"state": "closed", "labels": []string{}}), false},
		{"open, human-labeled", mk(map[string]any{"state": "open", "labels": []string{"human"}}), true},
		{"open, not human-labeled", mk(map[string]any{"state": "open", "labels": []string{}}), false},
		{"mixed: one open unlabeled taints it", mk(
			map[string]any{"state": "open", "labels": []string{"human"}},
			map[string]any{"state": "open", "labels": []string{}},
		), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeWaitingOnMe(tt.raw); got != tt.want {
				t.Errorf("computeWaitingOnMe(%s) = %v; want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// --- disposition rule set + override-wins acceptance criterion ---

func TestComputeDispositions(t *testing.T) {
	pr := prShow{
		Comments: []prComment{{ID: "c1", Resolved: false}, {ID: "c2", Resolved: true}},
		Reviews:  []prReview{{Comments: []prComment{{ID: "c3", Resolved: false}}}},
	}
	got := computeDispositions(pr)
	want := []Disposition{
		{CommentID: "c1", Verdict: DispositionOpen},
		{CommentID: "c2", Verdict: DispositionNoAction},
		{CommentID: "c3", Verdict: DispositionOpen},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("computeDispositions = %+v; want %+v", got, want)
	}
}

// TestDispositionOverrideSurvivesRerun is this packet's own pinned
// acceptance criterion: "A disposition recorded through pg-desk feedback
// set ... is an override the rule set MUST honor over its own verdict" and
// "Disposition overrides survive a re-run and win over the rule set."
func TestDispositionOverrideSurvivesRerun(t *testing.T) {
	pr := prShow{Comments: []prComment{{ID: "c1", Resolved: false}}}
	overrides := map[string]string{"c1": DispositionWillFix}

	base1 := computeDispositions(pr)
	if base1[0].Verdict != DispositionOpen {
		t.Fatalf("base rule set verdict = %q; want open (precondition for this test)", base1[0].Verdict)
	}
	merged1 := ApplyDispositionOverrides(base1, overrides)

	// Simulate a second pipeline run over the identical facts: the rule set
	// recomputes its verdict from scratch (still "open"), and the override
	// is re-applied on top of it.
	base2 := computeDispositions(pr)
	merged2 := ApplyDispositionOverrides(base2, overrides)

	if !reflect.DeepEqual(merged1, merged2) {
		t.Fatalf("override did not survive a re-run: run1=%+v run2=%+v", merged1, merged2)
	}
	if merged1[0].Verdict != DispositionWillFix || !merged1[0].Overridden {
		t.Fatalf("override did not win over the rule set's own verdict: %+v", merged1[0])
	}

	// The base slice itself must be untouched (ApplyDispositionOverrides
	// must not mutate its input).
	if base1[0].Verdict != DispositionOpen || base1[0].Overridden {
		t.Fatalf("ApplyDispositionOverrides mutated its base input: %+v", base1[0])
	}
}

func TestApplyDispositionOverrides_UnknownCommentIgnored(t *testing.T) {
	base := []Disposition{{CommentID: "c1", Verdict: DispositionOpen}}
	got := ApplyDispositionOverrides(base, map[string]string{"nonexistent": DispositionWontFix})
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("got %+v; want unchanged %+v", got, base)
	}
}

// --- panel placement / ready-to-promote (adapted from
// internal/snapshot/mine_panels.go & panels.go) ---

func TestClassifyPanel(t *testing.T) {
	tests := []struct {
		name string
		own  Ownership
		pr   prShow
		ci   ciRollupResult
		appr Approvals
		want string
	}{
		{"mine, conflict -> act now", OwnershipMine, prShow{Mergeable: "CONFLICTING"}, ciRollupResult{State: "success"}, Approvals{}, PanelMineActNow},
		{"mine, ci red -> act now", OwnershipMine, prShow{}, ciRollupResult{State: "failure"}, Approvals{}, PanelMineActNow},
		{"mine, bot disapproved -> act now", OwnershipMine, prShow{}, ciRollupResult{State: "success"}, Approvals{BotVerdict: BotVerdictDisapproved}, PanelMineActNow},
		{"mine, approved+clean+pending -> awaiting other things", OwnershipMine, prShow{MergeStateStatus: "CLEAN"}, ciRollupResult{State: "pending"}, Approvals{HumanApproved: true}, PanelMineAwaitingOtherThings},
		{"mine, otherwise clean -> awaiting others", OwnershipMine, prShow{MergeStateStatus: "CLEAN"}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingOthers},
		{"mine, merged -> none", OwnershipMine, prShow{Merged: true}, ciRollupResult{State: "success"}, Approvals{}, PanelNone},
		{"co-owned acts as mine", OwnershipCoOwned, prShow{Mergeable: "CONFLICTING"}, ciRollupResult{}, Approvals{}, PanelMineActNow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(tt.own, tt.pr, tt.ci, tt.appr, nil); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}

	t.Run("team, draft -> none", func(t *testing.T) {
		if got := classifyPanel(OwnershipTeam, prShow{Draft: true}, ciRollupResult{State: "success"}, Approvals{}, []string{MatchReasonTeamAuthored}); got != PanelNone {
			t.Errorf("got %q; want none", got)
		}
	})
	t.Run("team, no match reasons -> none", func(t *testing.T) {
		if got := classifyPanel(OwnershipTeam, prShow{}, ciRollupResult{State: "success"}, Approvals{}, nil); got != PanelNone {
			t.Errorf("got %q; want none", got)
		}
	})
	t.Run("team, clean -> act now", func(t *testing.T) {
		if got := classifyPanel(OwnershipTeam, prShow{}, ciRollupResult{State: "success"}, Approvals{}, []string{MatchReasonTeamAuthored}); got != PanelTeamActNow {
			t.Errorf("got %q; want team_act_now", got)
		}
	})
	t.Run("team, ci failing -> blocked", func(t *testing.T) {
		if got := classifyPanel(OwnershipTeam, prShow{}, ciRollupResult{State: "failure"}, Approvals{}, []string{MatchReasonTeamAuthored}); got != PanelTeamBlocked {
			t.Errorf("got %q; want team_blocked", got)
		}
	})
}

func TestComputeReadyToPromote(t *testing.T) {
	tests := []struct {
		name string
		own  Ownership
		pr   prShow
		ci   ciRollupResult
		appr Approvals
		want bool
	}{
		{"own draft, checks green -> ready", OwnershipMine, prShow{Draft: true}, ciRollupResult{State: "success"}, Approvals{}, true},
		{"co-owned excluded", OwnershipCoOwned, prShow{Draft: true}, ciRollupResult{State: "success"}, Approvals{}, false},
		{"not draft excluded", OwnershipMine, prShow{Draft: false}, ciRollupResult{State: "success"}, Approvals{}, false},
		{"conflict excluded", OwnershipMine, prShow{Draft: true, Mergeable: "CONFLICTING"}, ciRollupResult{State: "success"}, Approvals{}, false},
		{"ci not green excluded", OwnershipMine, prShow{Draft: true}, ciRollupResult{State: "pending"}, Approvals{}, false},
		{"bot disapproval excluded", OwnershipMine, prShow{Draft: true}, ciRollupResult{State: "success"}, Approvals{BotVerdict: BotVerdictDisapproved}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeReadyToPromote(tt.own, tt.pr, tt.ci, tt.appr); got != tt.want {
				t.Errorf("computeReadyToPromote = %v; want %v", got, tt.want)
			}
		})
	}
}

// --- top-level Interpret: degraded passthrough + deterministic clock ---

func factsFor(t *testing.T, pr map[string]any) gather.Facts {
	t.Helper()
	raw, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal pr show fixture: %v", err)
	}
	return gather.Facts{PRShow: raw, HeadSHA: "deadbeef"}
}

func TestInterpret_RemovedNotFound_Degrades(t *testing.T) {
	facts := gather.Facts{RemovedState: "not_found"}
	interp, err := Interpret(facts, FixedClock(time.Unix(0, 0)), &config.Config{})
	if err != nil {
		t.Fatalf("Interpret returned error for an empty (not_found) Facts: %v", err)
	}
	if interp.Ownership != "" || interp.Panel != "" || len(interp.MatchReasons) != 0 {
		t.Fatalf("expected a near-empty Interpretation, got %+v", interp)
	}
}

func TestInterpret_DegradedPassthrough(t *testing.T) {
	facts := factsFor(t, map[string]any{"author": "me", "title": "x"})
	facts.Degraded = "ci list"
	cfg := &config.Config{SelfLogin: "me"}
	interp, err := Interpret(facts, FixedClock(time.Unix(0, 0)), cfg)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if interp.Degraded != "ci list" {
		t.Fatalf("Degraded = %q; want %q (verbatim copy of Facts.Degraded)", interp.Degraded, "ci list")
	}
}

// TestInterpret_DeterministicWithFixedClock is this packet's own pinned
// acceptance criterion: "same input + same injected time => byte-identical
// output across two runs."
func TestInterpret_DeterministicWithFixedClock(t *testing.T) {
	facts := factsFor(t, map[string]any{
		"author":             "me",
		"title":              "fix(store): handle nil",
		"body":               "production incident",
		"branch":             "fix/nil",
		"labels":             []string{"p0"},
		"additions":          40,
		"deletions":          5,
		"mergeable":          "MERGEABLE",
		"merge_state_status": "CLEAN",
		"review_requests":    []string{"teammate"},
		"comments":           []map[string]any{{"id": "c1", "author": "teammate", "resolved": false}},
		"reviews": []map[string]any{
			{"id": "r1", "author": "alice", "state": "APPROVED"},
		},
	})
	facts.PRCommits, _ = json.Marshal(map[string]any{"commits": []map[string]any{{"sha": "abc", "author": "me", "message": "fix: nil deref"}}})
	facts.PRFiles, _ = json.Marshal(map[string]any{"files": []map[string]any{{"path": "a.go"}, {"path": "b.py"}}})
	facts.CI, _ = json.Marshal(map[string]any{"runs": []map[string]any{{"name": "build", "status": "completed", "conclusion": "success"}}})

	cfg := &config.Config{
		SelfLogin:         "me",
		TeamMembers:       []string{"teammate"},
		ApproverAllowlist: []string{"policy-bot"},
	}
	clock := FixedClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))

	first, err := Interpret(facts, clock, cfg)
	if err != nil {
		t.Fatalf("Interpret (first run): %v", err)
	}
	second, err := Interpret(facts, clock, cfg)
	if err != nil {
		t.Fatalf("Interpret (second run): %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Interpret is not deterministic under a fixed clock:\nrun1=%+v\nrun2=%+v", first, second)
	}
	if first.AsOf != "2026-09-16T12:00:00Z" {
		t.Fatalf("AsOf = %q; want the injected clock's own RFC3339 stamp", first.AsOf)
	}
	if first.Ownership != string(OwnershipMine) {
		t.Fatalf("Ownership = %q; want mine", first.Ownership)
	}
	if first.Urgency.Level != "high" {
		t.Fatalf("Urgency.Level = %q; want high (label p0 + keyword)", first.Urgency.Level)
	}
}

func TestInterpret_MalformedPRShow_Errors(t *testing.T) {
	facts := gather.Facts{PRShow: json.RawMessage(`{not valid json`)}
	if _, err := Interpret(facts, SystemClock{}, &config.Config{}); err == nil {
		t.Fatal("expected an error decoding a malformed (but non-empty) PRShow")
	}
}
