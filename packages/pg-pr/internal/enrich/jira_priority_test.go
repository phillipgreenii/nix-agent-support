package enrich

// Package enrich — jira_priority_test.go
//
// TDD tests for the Jira priority/incident urgency signal (pg2-4c5i.26).
//
// Tests use a mock JiraLookupFunc — no real Jira client is wired here.
// Public-repo hygiene: all ticket keys in tests are generic (e.g. ABC-123).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// --- mock JiraLookupFunc ---

// mockJiraLookup is a test double for JiraLookupFunc.
type mockJiraLookup struct {
	// results maps ticket key to JiraTicketInfo; missing key → zero value (low priority, no incident).
	results map[string]JiraTicketInfo
	// err, when set, is returned for every lookup.
	err error
}

func (m *mockJiraLookup) Lookup(_ context.Context, key string) (JiraTicketInfo, error) {
	if m.err != nil {
		return JiraTicketInfo{}, m.err
	}
	return m.results[key], nil
}

// --- JiraTicketInfo field tests ---

func TestJiraTicketInfo_zero_value_is_low_priority(t *testing.T) {
	// A zero JiraTicketInfo (not found or unknown) MUST NOT fire the signal.
	var info JiraTicketInfo
	if info.HighPriority {
		t.Error("zero JiraTicketInfo should have HighPriority=false")
	}
	if info.ActiveIncident {
		t.Error("zero JiraTicketInfo should have ActiveIncident=false")
	}
}

// --- scoreJiraPriority unit tests ---

func TestScoreJiraPriority_nil_func_is_noop(t *testing.T) {
	in := Input{
		PR:               api.PR{Title: "fix: something", Branch: "fix/thing"},
		LinkedTicketKeys: []string{"ABC-123"},
		JiraLookupFunc:   nil, // explicitly nil
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 with nil JiraLookupFunc; got %d", score)
	}
	if len(reasons) != 0 {
		t.Errorf("want no reasons with nil JiraLookupFunc; got %v", reasons)
	}
}

func TestScoreJiraPriority_no_linked_tickets_is_noop(t *testing.T) {
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"ABC-123": {HighPriority: true},
	}}
	in := Input{
		PR:               api.PR{Title: "fix: something"},
		LinkedTicketKeys: nil, // no tickets
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 with no linked tickets; got %d", score)
	}
	if len(reasons) != 0 {
		t.Errorf("want no reasons with no linked tickets; got %v", reasons)
	}
}

func TestScoreJiraPriority_high_priority_ticket_fires(t *testing.T) {
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"ABC-123": {HighPriority: true, ActiveIncident: false},
	}}
	in := Input{
		PR:               api.PR{Title: "fix: something", Branch: "fix/thing"},
		LinkedTicketKeys: []string{"ABC-123"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score <= 0 {
		t.Errorf("want score>0 for high-priority ticket; got %d", score)
	}
	found := false
	for _, r := range reasons {
		if r == "jira-priority:ABC-123" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want reason jira-priority:ABC-123; got %v", reasons)
	}
}

func TestScoreJiraPriority_active_incident_fires(t *testing.T) {
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"INC-99": {HighPriority: false, ActiveIncident: true},
	}}
	in := Input{
		PR:               api.PR{Title: "hotfix: rollback", Branch: "hotfix/rollback"},
		LinkedTicketKeys: []string{"INC-99"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score <= 0 {
		t.Errorf("want score>0 for active incident; got %d", score)
	}
	found := false
	for _, r := range reasons {
		if r == "jira-incident:INC-99" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want reason jira-incident:INC-99; got %v", reasons)
	}
}

func TestScoreJiraPriority_normal_ticket_does_not_fire(t *testing.T) {
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"FEAT-42": {HighPriority: false, ActiveIncident: false},
	}}
	in := Input{
		PR:               api.PR{Title: "feat: new thing", Branch: "feat/new-thing"},
		LinkedTicketKeys: []string{"FEAT-42"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 for normal ticket; got %d", score)
	}
	for _, r := range reasons {
		if strings.HasPrefix(r, "jira-priority:") || strings.HasPrefix(r, "jira-incident:") {
			t.Errorf("want no jira reasons for normal ticket; got reason %q", r)
		}
	}
}

func TestScoreJiraPriority_lookup_error_does_not_fire(t *testing.T) {
	mock := &mockJiraLookup{err: errors.New("connection refused")}
	in := Input{
		PR:               api.PR{Title: "fix: something"},
		LinkedTicketKeys: []string{"ABC-123"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score != 0 {
		t.Errorf("want score=0 on lookup error; got %d", score)
	}
	if len(reasons) != 0 {
		t.Errorf("want no reasons on lookup error; got %v", reasons)
	}
}

func TestScoreJiraPriority_both_high_priority_and_incident_fire_combined(t *testing.T) {
	// A ticket that is BOTH high-priority AND an active incident must yield
	// score==6 (incident +4 + priority +2) and include BOTH reason strings.
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"INC-1": {HighPriority: true, ActiveIncident: true},
	}}
	in := Input{
		PR:               api.PR{Title: "fix: critical", Branch: "fix/critical"},
		LinkedTicketKeys: []string{"INC-1"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score != 6 {
		t.Errorf("want score==6 for incident(+4)+priority(+2) ticket; got %d", score)
	}
	// Both reason strings must be present.
	hasIncident := false
	hasPriority := false
	for _, r := range reasons {
		if r == "jira-incident:INC-1" {
			hasIncident = true
		}
		if r == "jira-priority:INC-1" {
			hasPriority = true
		}
	}
	if !hasIncident {
		t.Errorf("want reason jira-incident:INC-1 present; got %v", reasons)
	}
	if !hasPriority {
		t.Errorf("want reason jira-priority:INC-1 present; got %v", reasons)
	}
}

func TestScoreJiraPriority_multi_ticket_only_firing_ones_add_score(t *testing.T) {
	// Two tickets: one high-priority, one normal. Only the high-priority one fires.
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"ABC-1": {HighPriority: true},
		"ABC-2": {HighPriority: false},
	}}
	in := Input{
		PR:               api.PR{Title: "fix: multi", Branch: "fix/multi"},
		LinkedTicketKeys: []string{"ABC-1", "ABC-2"},
		JiraLookupFunc:   mock.Lookup,
	}
	score, reasons := scoreJiraPriority(context.Background(), in)
	if score <= 0 {
		t.Errorf("want score>0 when at least one high-priority ticket; got %d", score)
	}
	// ABC-1 should fire; ABC-2 should not.
	abcOneFound := false
	abcTwoFound := false
	for _, r := range reasons {
		if r == "jira-priority:ABC-1" {
			abcOneFound = true
		}
		if strings.Contains(r, "ABC-2") {
			abcTwoFound = true
		}
	}
	if !abcOneFound {
		t.Errorf("want reason jira-priority:ABC-1; got %v", reasons)
	}
	if abcTwoFound {
		t.Errorf("want no reason for ABC-2 (normal ticket); got %v", reasons)
	}
}

// --- Integration: scoreUrgencyWithHealth + Jira ---

func TestScoreUrgencyWithJira_incident_reaches_high(t *testing.T) {
	// An otherwise-low-urgency PR with a linked active-incident ticket should
	// reach high urgency from the Jira signal alone (+4 for incident ≥ threshold 3).
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"INC-5": {ActiveIncident: true},
	}}
	in := Input{
		PR:               api.PR{Title: "boring refactor", Branch: "refactor/boring"},
		LinkedTicketKeys: []string{"INC-5"},
		JiraLookupFunc:   mock.Lookup,
	}
	lvl, score, reasons := scoreUrgencyWithHealth(context.Background(), in)
	if lvl != "high" {
		t.Errorf("want high urgency for linked active-incident; got %q (score=%d reasons=%v)", lvl, score, reasons)
	}
	found := false
	for _, r := range reasons {
		if r == "jira-incident:INC-5" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want jira-incident:INC-5 reason; got %v", reasons)
	}
}

func TestScoreUrgencyWithJira_high_priority_reaches_medium_or_higher(t *testing.T) {
	// A PR linked to a high-priority ticket (but not incident) should reach at
	// least medium urgency from the Jira signal (+2 for priority ≥ threshold 1).
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"BUG-7": {HighPriority: true},
	}}
	in := Input{
		PR:               api.PR{Title: "boring refactor", Branch: "refactor/boring"},
		LinkedTicketKeys: []string{"BUG-7"},
		JiraLookupFunc:   mock.Lookup,
	}
	lvl, score, reasons := scoreUrgencyWithHealth(context.Background(), in)
	if lvl == "low" {
		t.Errorf("want medium or high urgency for linked high-priority ticket; got %q (score=%d reasons=%v)", lvl, score, reasons)
	}
	_ = score
	_ = reasons
}

func TestScoreUrgencyWithJira_nil_func_no_regression(t *testing.T) {
	// Existing urgency scoring MUST be unaffected when JiraLookupFunc is nil.
	in := Input{
		PR:               api.PR{Title: "fix(api): null deref", Branch: "fix/null"},
		LinkedTicketKeys: []string{"ABC-999"},
		JiraLookupFunc:   nil,
	}
	lvl, score, reasons := scoreUrgencyWithHealth(context.Background(), in)
	// Should behave identically to the original scoreUrgency call.
	wantLvl, wantScore, wantReasons := scoreUrgency(in)
	if lvl != wantLvl || score != wantScore {
		t.Errorf("nil JiraLookupFunc changed urgency: got %q/%d vs want %q/%d reasons=%v",
			lvl, score, wantLvl, wantScore, reasons)
	}
	_ = wantReasons
}

// --- Compute / ComputeWithContext integration ---

func TestComputeWithJira_urgency_factors_in_linked_incident(t *testing.T) {
	ctx := context.Background()
	mock := &mockJiraLookup{results: map[string]JiraTicketInfo{
		"INC-10": {ActiveIncident: true},
	}}
	in := Input{
		PR:               api.PR{Title: "refactor: cleanup", Branch: "refactor/cleanup", Additions: 10, Deletions: 2},
		Files:            []string{"svc/alpha/main.go"},
		LinkedTicketKeys: []string{"INC-10"},
		JiraLookupFunc:   mock.Lookup,
	}
	got := ComputeWithContext(ctx, in)
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high (linked active-incident ticket)", got.Urgency)
	}
	found := false
	for _, r := range got.UrgencyReasons {
		if r == "jira-incident:INC-10" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("UrgencyReasons = %v; want to include jira-incident:INC-10", got.UrgencyReasons)
	}
}

func TestComputeWithJira_nil_jira_func_no_regression(t *testing.T) {
	// Compute with no JiraLookupFunc and no ProjectHealthFunc must match pre-bead baseline.
	// Uses a strong urgency label (p0 → +3) to reach "high" without Jira.
	ctx := context.Background()
	in := Input{
		PR:     api.PR{Title: "fix(api): null deref", Additions: 40, Deletions: 5, Branch: "fix/null"},
		Files:  []string{"a.go", "b.go"},
		Labels: []string{"p0"},
	}
	got := ComputeWithContext(ctx, in)
	// Should reach high from p0 label — unchanged from before pg2-4c5i.26.
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high (p0 label)", got.Urgency)
	}
}
