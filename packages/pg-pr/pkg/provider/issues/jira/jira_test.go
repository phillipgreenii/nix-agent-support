package jira_test

import (
	"context"
	"errors"
	"testing"

	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
)

// fakeRunner implements jiraprovider.Runner for tests. It records the argv it
// was handed and returns canned output.
type fakeRunner struct {
	gotArgv []string
	stdout  []byte
	err     error
}

func (f *fakeRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	f.gotArgv = argv
	return f.stdout, f.err
}

func TestGetIssue_mapsJSONToAPIIssue(t *testing.T) {
	const cliJSON = `{
	  "key":"ENG-42","summary":"A title","status":"In Progress",
	  "issuetype":"Bug","labels":["urgent"],"url":"https://x.atlassian.net/browse/ENG-42",
	  "priority":"High"
	}`
	p := jiraprovider.NewWithRunner("jira", &fakeRunner{stdout: []byte(cliJSON)})
	got, err := p.GetIssue(context.Background(), "ENG-42")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.ID != "ENG-42" {
		t.Errorf("ID = %q, want ENG-42", got.ID)
	}
	if got.Title != "A title" {
		t.Errorf("Title = %q, want \"A title\"", got.Title)
	}
	if got.State != "In Progress" {
		t.Errorf("State = %q, want \"In Progress\"", got.State)
	}
	if got.URL != "https://x.atlassian.net/browse/ENG-42" {
		t.Errorf("URL = %q", got.URL)
	}
}

func TestGetIssue_execsBinaryIssueKey(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`{"key":"ENG-7","url":"u"}`)}
	p := jiraprovider.NewWithRunner("my-jira-bin", r)
	if _, err := p.GetIssue(context.Background(), "ENG-7"); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	want := []string{"my-jira-bin", "issue", "ENG-7"}
	if len(r.gotArgv) != len(want) {
		t.Fatalf("argv = %v, want %v", r.gotArgv, want)
	}
	for i := range want {
		if r.gotArgv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", r.gotArgv, want)
		}
	}
}

func TestGetIssue_emptyKeyErrorsWithoutExec(t *testing.T) {
	r := &fakeRunner{}
	p := jiraprovider.NewWithRunner("jira", r)
	if _, err := p.GetIssue(context.Background(), "   "); err == nil {
		t.Fatal("want error on empty key")
	}
	if r.gotArgv != nil {
		t.Errorf("runner should not be invoked for empty key; got argv %v", r.gotArgv)
	}
}

func TestGetIssue_runnerErrorPropagates(t *testing.T) {
	p := jiraprovider.NewWithRunner("jira", &fakeRunner{err: errors.New("exit 1")})
	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
		t.Fatal("want error when runner fails")
	}
}

func TestGetIssue_invalidJSONErrors(t *testing.T) {
	p := jiraprovider.NewWithRunner("jira", &fakeRunner{stdout: []byte("not-json")})
	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
		t.Fatal("want error on invalid JSON")
	}
}

func TestGetIssue_missingKeyErrors(t *testing.T) {
	// The generic tool should always set "key"; treat a missing key as an error.
	p := jiraprovider.NewWithRunner("jira", &fakeRunner{stdout: []byte(`{"summary":"s","status":"Open"}`)})
	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
		t.Fatal("want error when JSON is missing key field")
	}
}

func TestNew_defaultBinaryIsJira(t *testing.T) {
	t.Setenv("PGPR_JIRA_BINARY", "")
	if got := jiraprovider.New().Binary(); got != "jira" {
		t.Errorf("default binary = %q, want jira", got)
	}
}

func TestNew_respectsEnvBinaryName(t *testing.T) {
	t.Setenv("PGPR_JIRA_BINARY", "pg-pr-issues-jira-zr")
	if got := jiraprovider.New().Binary(); got != "pg-pr-issues-jira-zr" {
		t.Errorf("env binary = %q, want pg-pr-issues-jira-zr", got)
	}
}
