package sync

// enrich_jira_wiring_test.go — TDD tests for the JiraLookupFunc wiring in
// enrichAndStore (pg2-jpfw.4).
//
// Verifies:
//  1. When JiraConfig is not set in config, in.JiraLookupFunc remains nil
//     (backward-compatible: no behavior change).
//  2. When JiraConfig IS set, the adapter is wired and the lookup function
//     is called for linked ticket keys.
//
// Uses a fake provider (fakeIssuesProvider) to avoid real Jira subprocess calls.
// Public-repo hygiene: no org-specific URLs or keys.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
)

// fakeJiraRunner is a jiraprovider.Runner that returns canned JSON for specific keys.
type fakeJiraRunner struct {
	responses map[string][]byte // key → stdout JSON
	err       error
}

func (f *fakeJiraRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	// argv is ["jira", "issue", "<key>"]
	if len(argv) < 3 {
		return nil, nil
	}
	key := argv[2]
	if b, ok := f.responses[key]; ok {
		return b, nil
	}
	// Default: return a valid but low-priority issue.
	return []byte(`{"key":"` + key + `","summary":"stub","status":"Open","url":"u"}`), nil
}

// highPriorityJSON returns a fake CLI JSON with a high-priority label.
func highPriorityJSON(key string) []byte {
	return []byte(`{"key":"` + key + `","summary":"bug","status":"Open","url":"u","priority":"High","issuetype":"Bug","labels":[]}`)
}

// incidentJSON returns a fake CLI JSON with an incident label.
func incidentJSON(key string) []byte {
	return []byte(`{"key":"` + key + `","summary":"outage","status":"Open","url":"u","priority":"Medium","issuetype":"Task","labels":["incident"]}`)
}

// TestEnrichAndStore_JiraUnconfigured_LookupFuncRemainsNil verifies that
// when config.Jira is nil (unconfigured), the JiraLookupFunc is NOT wired
// and enrichment proceeds unchanged (backward-compatible).
func TestEnrichAndStore_JiraUnconfigured_LookupFuncRemainsNil(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pr := api.PR{
		Repo: "o/r", Number: 10, Title: "refactor: cleanup", Branch: "refactor/cleanup",
		Author: "me", State: "open", Additions: 5, Deletions: 2,
	}
	if _, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 10, Ownership: "mine", Author: "me", State: "open"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Config has NO Jira section.
	cfg := &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: t.TempDir(),
		Repos:        []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		// Jira field intentionally absent.
	}
	e := &Engine{deps: Deps{
		Cfg:   cfg,
		Store: db,
		Now:   func() time.Time { return time.Unix(0, 0).UTC() },
	}}

	rcfg := config.RepoConfig{
		Remote:         "o/r",
		TicketPatterns: []string{`[A-Z]+-\d+`},
	}
	// PR body carries a ticket key — but since Jira is unconfigured, urgency
	// must NOT change from the base (no Jira bump).
	pr.Body = "Fixes ABC-123"
	pr.Title = "refactor: cleanup"
	if err := e.enrichAndStore(ctx, "o/r", pr, nil, rcfg); err != nil {
		t.Fatalf("enrichAndStore: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 10)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	// Without Jira configured, no jira-priority/jira-incident reasons present.
	for _, r := range got.UrgencyReasons {
		if len(r) > 5 && r[:5] == "jira-" {
			t.Errorf("jira reason %q present but Jira is unconfigured; want no Jira signal", r)
		}
	}
}

// TestEnrichAndStore_JiraConfigured_HighPriorityTicket_UrgencyBumped verifies
// that when config.Jira is set and a linked ticket has high priority, the
// urgency score is bumped.
func TestEnrichAndStore_JiraConfigured_HighPriorityTicket_UrgencyBumped(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pr := api.PR{
		Repo: "o/r", Number: 11, Title: "fix: small patch", Branch: "fix/small",
		Body:      "Refs ABC-999",
		Author:    "me",
		State:     "open",
		Additions: 3,
		Deletions: 1,
	}
	if _, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 11, Ownership: "mine", Author: "me", State: "open"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	runner := &fakeJiraRunner{
		responses: map[string][]byte{
			"ABC-999": highPriorityJSON("ABC-999"),
		},
	}
	jiraProvider := jiraprovider.NewWithRunner("jira", runner)

	cfg := &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: t.TempDir(),
		Repos:        []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		Jira: &config.JiraConfig{
			AdapterConfig: jiraprovider.AdapterConfig{
				HighPriorityValues: []string{"High", "Highest"},
			},
		},
	}
	e := &Engine{deps: Deps{
		Cfg:          cfg,
		Store:        db,
		Now:          func() time.Time { return time.Unix(0, 0).UTC() },
		JiraProvider: jiraProvider,
	}}

	rcfg := config.RepoConfig{
		Remote:         "o/r",
		TicketPatterns: []string{`[A-Z]+-\d+`},
	}
	if err := e.enrichAndStore(ctx, "o/r", pr, nil, rcfg); err != nil {
		t.Fatalf("enrichAndStore: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 11)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	// Expect at least one jira-priority reason.
	found := false
	for _, r := range got.UrgencyReasons {
		if r == "jira-priority:ABC-999" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want jira-priority:ABC-999 in UrgencyReasons; got %v", got.UrgencyReasons)
	}
}

// TestEnrichAndStore_JiraConfigured_IncidentTicket_UrgencyHigh verifies that
// an active-incident ticket bumps urgency to "high".
func TestEnrichAndStore_JiraConfigured_IncidentTicket_UrgencyHigh(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pr := api.PR{
		Repo: "o/r", Number: 12, Title: "chore: boring", Branch: "chore/boring",
		Body:      "Related to INC-42",
		Author:    "me",
		State:     "open",
		Additions: 1,
		Deletions: 0,
	}
	if _, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 12, Ownership: "mine", Author: "me", State: "open"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	runner := &fakeJiraRunner{
		responses: map[string][]byte{
			"INC-42": incidentJSON("INC-42"),
		},
	}
	jiraProvider := jiraprovider.NewWithRunner("jira", runner)

	cfg := &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: t.TempDir(),
		Repos:        []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		Jira: &config.JiraConfig{
			AdapterConfig: jiraprovider.AdapterConfig{
				HighPriorityValues: []string{"High", "Highest"},
				IncidentLabels:     []string{"incident"},
			},
		},
	}
	e := &Engine{deps: Deps{
		Cfg:          cfg,
		Store:        db,
		Now:          func() time.Time { return time.Unix(0, 0).UTC() },
		JiraProvider: jiraProvider,
	}}

	rcfg := config.RepoConfig{
		Remote:         "o/r",
		TicketPatterns: []string{`[A-Z]+-\d+`},
	}
	if err := e.enrichAndStore(ctx, "o/r", pr, nil, rcfg); err != nil {
		t.Fatalf("enrichAndStore: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 12)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high for linked active-incident ticket", got.Urgency)
	}
	found := false
	for _, r := range got.UrgencyReasons {
		if r == "jira-incident:INC-42" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("want jira-incident:INC-42 in UrgencyReasons; got %v", got.UrgencyReasons)
	}
}
