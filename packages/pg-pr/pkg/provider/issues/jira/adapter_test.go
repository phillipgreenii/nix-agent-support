package jira_test

// adapter_test.go — TDD tests for the JiraLookupFunc adapter (pg2-jpfw.4).
//
// The adapter wraps the in-repo Jira provider and maps api.Issue fields
// (Priority, Labels, IssueType) to enrich.JiraTicketInfo. Tests use a fake
// runner (defined in jira_test.go's file scope) to avoid real Jira calls.
//
// Public-repo hygiene: all ticket keys and issue data use generic/fictional
// values (e.g. ABC-123, "Highest", "incident"). No org-specific Jira URLs or
// project keys appear here.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
)

// cliIssueJSON encodes a fake jira CLI JSON response as the subprocess would
// emit it (the shape cliIssue in jira.go parses).
func cliIssueJSON(key, priority, issueType string, labels []string) []byte {
	type payload struct {
		Key       string   `json:"key"`
		Summary   string   `json:"summary"`
		Status    string   `json:"status"`
		IssueType string   `json:"issuetype"`
		Labels    []string `json:"labels"`
		URL       string   `json:"url"`
		Priority  string   `json:"priority,omitempty"`
	}
	b, _ := json.Marshal(payload{
		Key:       key,
		Summary:   "Some issue",
		Status:    "In Progress",
		IssueType: issueType,
		Labels:    labels,
		URL:       "https://example.atlassian.net/browse/" + key,
		Priority:  priority,
	})
	return b
}

// --- NewJiraLookupFunc adapter tests ---

func TestNewJiraLookupFunc_highPriorityValue_setsHighPriority(t *testing.T) {
	runner := &fakeRunner{stdout: cliIssueJSON("ABC-123", "Highest", "Task", nil)}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"Highest", "High"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)
	if fn == nil {
		t.Fatal("NewJiraLookupFunc returned nil")
	}

	info, err := fn(context.Background(), "ABC-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.HighPriority {
		t.Errorf("HighPriority = false; want true for priority %q", "Highest")
	}
	if info.ActiveIncident {
		t.Errorf("ActiveIncident = true; want false (no incident label/type)")
	}
}

func TestNewJiraLookupFunc_nonHighPriorityValue_noHighPriority(t *testing.T) {
	runner := &fakeRunner{stdout: cliIssueJSON("ABC-456", "Medium", "Task", nil)}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"Highest", "High"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "ABC-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.HighPriority {
		t.Errorf("HighPriority = true; want false for priority %q", "Medium")
	}
}

func TestNewJiraLookupFunc_incidentLabel_setsActiveIncident(t *testing.T) {
	runner := &fakeRunner{stdout: cliIssueJSON("INC-1", "Medium", "Task", []string{"incident", "production"})}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"Highest", "High"},
		IncidentLabels:     []string{"incident"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "INC-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.ActiveIncident {
		t.Errorf("ActiveIncident = false; want true for incident label")
	}
}

func TestNewJiraLookupFunc_incidentIssueType_setsActiveIncident(t *testing.T) {
	runner := &fakeRunner{stdout: cliIssueJSON("INC-2", "High", "Incident", nil)}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues:  []string{"Highest", "High"},
		IncidentIssueTypes: []string{"Incident"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "INC-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.ActiveIncident {
		t.Errorf("ActiveIncident = false; want true for incident issue type")
	}
	if !info.HighPriority {
		t.Errorf("HighPriority = false; want true for High priority")
	}
}

func TestNewJiraLookupFunc_normalTicket_neitherFires(t *testing.T) {
	runner := &fakeRunner{stdout: cliIssueJSON("FEAT-99", "Low", "Story", []string{"enhancement"})}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"Highest", "High"},
		IncidentLabels:     []string{"incident"},
		IncidentIssueTypes: []string{"Incident"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "FEAT-99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.HighPriority || info.ActiveIncident {
		t.Errorf("want zero JiraTicketInfo for normal ticket; got %+v", info)
	}
}

func TestNewJiraLookupFunc_providerError_returnsError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("connection refused")}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"High"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	_, err := fn(context.Background(), "ABC-1")
	if err == nil {
		t.Fatal("want error when provider fails; got nil")
	}
}

func TestNewJiraLookupFunc_emptyConfig_highPriorityValuesNilNoSignal(t *testing.T) {
	// When HighPriorityValues is nil/empty, HighPriority should never fire
	// regardless of the ticket's priority field.
	runner := &fakeRunner{stdout: cliIssueJSON("ABC-999", "Highest", "Task", nil)}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{} // empty: no high-priority values configured
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "ABC-999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.HighPriority {
		t.Errorf("HighPriority = true; want false when HighPriorityValues not configured")
	}
}

func TestNewJiraLookupFunc_priorityMatchIsCaseInsensitive(t *testing.T) {
	// Priority matching MUST be case-insensitive so configs can use either "high"
	// or "High" without surprises.
	runner := &fakeRunner{stdout: cliIssueJSON("ABC-77", "HIGH", "Task", nil)}
	p := jiraprovider.NewWithRunner("jira", runner)
	cfg := jiraprovider.AdapterConfig{
		HighPriorityValues: []string{"high"},
	}
	fn := jiraprovider.NewJiraLookupFunc(p, cfg)

	info, err := fn(context.Background(), "ABC-77")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.HighPriority {
		t.Errorf("HighPriority = false; want true for case-insensitive match (ticket HIGH vs config high)")
	}
}
