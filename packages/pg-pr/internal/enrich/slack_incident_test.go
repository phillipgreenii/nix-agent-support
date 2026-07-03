package enrich

// Package enrich — slack_incident_test.go
//
// TDD tests for the Slack production-incident cross-reference urgency signal
// (pg2-4c5i.27).
//
// Design: mirrors jira_priority_test.go exactly. Tests use a mock
// SlackIncidentFunc — no real Slack client or LLM is wired here.
// Public-repo hygiene: no org-specific workspace/channel IDs appear; all
// test data is generic/fictional.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// --- mock SlackIncidentFunc ---

// mockSlackIncident is a test double for SlackIncidentFunc.
type mockSlackIncident struct {
	// info is returned when err is nil.
	info SlackIncidentInfo
	// err, when non-nil, is returned instead of info.
	err error
}

func (m *mockSlackIncident) Lookup(_ context.Context, _ SlackIncidentQuery) (SlackIncidentInfo, error) {
	if m.err != nil {
		return SlackIncidentInfo{}, m.err
	}
	return m.info, nil
}

// --- SlackIncidentInfo field tests ---

func TestSlackIncidentInfo_zero_value_does_not_fire(t *testing.T) {
	// A zero SlackIncidentInfo (no result / unknown) MUST NOT trigger the signal.
	var info SlackIncidentInfo
	if info.ActiveIncident {
		t.Error("zero SlackIncidentInfo should have ActiveIncident=false")
	}
}

// --- scoreSlackIncident unit tests ---

func TestScoreSlackIncident_nil_func_is_noop(t *testing.T) {
	in := Input{
		PR:                api.PR{Title: "fix: something", Branch: "fix/thing"},
		SlackIncidentFunc: nil, // explicitly nil
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 with nil SlackIncidentFunc; got %d", score)
	}
	if len(reasons) != 0 {
		t.Errorf("want no reasons with nil SlackIncidentFunc; got %v", reasons)
	}
}

func TestScoreSlackIncident_active_incident_fires(t *testing.T) {
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "INC-2024-0042",
	}}
	in := Input{
		PR:                api.PR{Title: "hotfix: rollback", Branch: "hotfix/rollback"},
		SlackIncidentFunc: mock.Lookup,
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score != 4 {
		t.Errorf("want score==4 for active Slack incident; got %d", score)
	}
	found := false
	for _, r := range reasons {
		if r == "slack-incident:INC-2024-0042" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want reason slack-incident:INC-2024-0042; got %v", reasons)
	}
}

func TestScoreSlackIncident_active_incident_reason_includes_ref(t *testing.T) {
	// The reason string MUST include the incident Ref when it is non-empty, so
	// the urgency report is traceable.
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "inc-ref-xyz",
	}}
	in := Input{
		PR:                api.PR{Title: "fix: thing", Branch: "fix/thing"},
		SlackIncidentFunc: mock.Lookup,
	}
	_, reasons := scoreSlackIncident(context.Background(), in)
	found := false
	for _, r := range reasons {
		if r == "slack-incident:inc-ref-xyz" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want reason slack-incident:inc-ref-xyz; got %v", reasons)
	}
}

func TestScoreSlackIncident_no_incident_does_not_fire(t *testing.T) {
	mock := &mockSlackIncident{info: SlackIncidentInfo{ActiveIncident: false}}
	in := Input{
		PR:                api.PR{Title: "feat: new thing", Branch: "feat/new-thing"},
		SlackIncidentFunc: mock.Lookup,
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 when no active incident; got %d", score)
	}
	for _, r := range reasons {
		if strings.HasPrefix(r, "slack-incident:") {
			t.Errorf("want no slack-incident reasons when no active incident; got reason %q", r)
		}
	}
}

func TestScoreSlackIncident_lookup_error_does_not_fire(t *testing.T) {
	mock := &mockSlackIncident{err: errors.New("slack API unavailable")}
	in := Input{
		PR:                api.PR{Title: "fix: something"},
		SlackIncidentFunc: mock.Lookup,
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 on lookup error; got %d", score)
	}
	if len(reasons) != 0 {
		t.Errorf("want no reasons on lookup error; got %v", reasons)
	}
}

func TestScoreSlackIncident_active_incident_bump_is_high_severity(t *testing.T) {
	// An active Slack production incident MUST bump urgency by exactly +4,
	// consistent with the pg2-4c5i.25 / .26 scale so it reaches "high" on its own.
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "inc-123",
	}}
	in := Input{
		PR:                api.PR{Title: "boring refactor", Branch: "refactor/boring"},
		SlackIncidentFunc: mock.Lookup,
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score != 4 {
		t.Errorf("want score==4 for active Slack incident (high-severity bump); got %d", score)
	}
	found := false
	for _, r := range reasons {
		if r == "slack-incident:inc-123" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want reason slack-incident:inc-123; got %v", reasons)
	}
}

func TestScoreSlackIncident_active_incident_empty_ref_still_fires(t *testing.T) {
	// When the LLM cannot extract a usable ref, the signal still fires;
	// the reason falls back to "slack-incident" (no colon-suffix).
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "", // no extractable ref
	}}
	in := Input{
		PR:                api.PR{Title: "fix: thing", Branch: "fix/thing"},
		SlackIncidentFunc: mock.Lookup,
	}
	score, reasons := scoreSlackIncident(context.Background(), in)
	if score <= 0 {
		t.Errorf("want score>0 even when Ref is empty; got %d", score)
	}
	found := false
	for _, r := range reasons {
		if strings.HasPrefix(r, "slack-incident") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want a slack-incident reason even with empty Ref; got %v", reasons)
	}
}

// --- Integration: scoreUrgencyWithHealth + Slack ---

func TestScoreUrgencyWithSlack_incident_reaches_high(t *testing.T) {
	// An otherwise-low-urgency PR with a related active Slack incident must
	// reach high urgency from the Slack signal alone (+4 ≥ threshold 3).
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "inc-prod-999",
	}}
	in := Input{
		PR:                api.PR{Title: "boring refactor", Branch: "refactor/boring"},
		SlackIncidentFunc: mock.Lookup,
	}
	lvl, score, reasons := scoreUrgencyWithHealth(context.Background(), in)
	if lvl != "high" {
		t.Errorf("want high urgency for active Slack incident; got %q (score=%d reasons=%v)", lvl, score, reasons)
	}
	found := false
	for _, r := range reasons {
		if strings.HasPrefix(r, "slack-incident:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want slack-incident: reason; got %v", reasons)
	}
}

func TestScoreUrgencyWithSlack_nil_func_no_regression(t *testing.T) {
	// Existing urgency scoring MUST be unaffected when SlackIncidentFunc is nil.
	in := Input{
		PR:                api.PR{Title: "fix(api): null deref", Branch: "fix/null"},
		SlackIncidentFunc: nil,
	}
	lvl, score, reasons := scoreUrgencyWithHealth(context.Background(), in)
	wantLvl, wantScore, wantReasons := scoreUrgency(in)
	if lvl != wantLvl || score != wantScore {
		t.Errorf("nil SlackIncidentFunc changed urgency: got %q/%d vs want %q/%d reasons=%v",
			lvl, score, wantLvl, wantScore, reasons)
	}
	_ = wantReasons
}

// --- Compute / ComputeWithContext integration ---

func TestComputeWithSlack_urgency_factors_in_active_incident(t *testing.T) {
	ctx := context.Background()
	mock := &mockSlackIncident{info: SlackIncidentInfo{
		ActiveIncident: true,
		Ref:            "inc-2024-0077",
	}}
	in := Input{
		PR:                api.PR{Title: "refactor: cleanup", Branch: "refactor/cleanup", Additions: 10, Deletions: 2},
		Files:             []string{"svc/alpha/main.go"},
		SlackIncidentFunc: mock.Lookup,
	}
	got := ComputeWithContext(ctx, in)
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high (active Slack incident)", got.Urgency)
	}
	found := false
	for _, r := range got.UrgencyReasons {
		if r == "slack-incident:inc-2024-0077" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("UrgencyReasons = %v; want to include slack-incident:inc-2024-0077", got.UrgencyReasons)
	}
}

func TestComputeWithSlack_nil_slack_func_no_regression(t *testing.T) {
	// Compute with no SlackIncidentFunc must match pre-bead baseline.
	// Uses a strong urgency label (p0 → +3) to reach "high" without Slack.
	ctx := context.Background()
	in := Input{
		PR:     api.PR{Title: "fix(api): null deref", Additions: 40, Deletions: 5, Branch: "fix/null"},
		Files:  []string{"a.go", "b.go"},
		Labels: []string{"p0"},
	}
	got := ComputeWithContext(ctx, in)
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high (p0 label, no Slack func)", got.Urgency)
	}
}

func TestComputeWithSlack_SlackIncidentQuery_carries_pr_context(t *testing.T) {
	// The SlackIncidentQuery passed to SlackIncidentFunc MUST carry PR context
	// so the LLM/Slack backend can search relevant channels.
	ctx := context.Background()
	var capturedQuery SlackIncidentQuery
	captureFunc := func(_ context.Context, q SlackIncidentQuery) (SlackIncidentInfo, error) {
		capturedQuery = q
		return SlackIncidentInfo{ActiveIncident: false}, nil
	}
	in := Input{
		PR: api.PR{
			Title:  "fix: database timeout",
			Body:   "Fixing the 30s timeout on the read replica",
			Branch: "fix/db-timeout",
		},
		SlackIncidentFunc: captureFunc,
	}
	ComputeWithContext(ctx, in)
	if capturedQuery.PRTitle == "" {
		t.Error("SlackIncidentQuery.PRTitle must be populated from Input.PR.Title")
	}
	if capturedQuery.PRTitle != in.PR.Title {
		t.Errorf("SlackIncidentQuery.PRTitle = %q; want %q", capturedQuery.PRTitle, in.PR.Title)
	}
}
