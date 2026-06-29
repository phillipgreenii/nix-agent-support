// Package jira is the built-in Jira issues provider for pg-pr.
//
// It delegates to the generic `jira issue <KEY>` CLI (from repo-base
// modules/jira/) via a subprocess call, then maps the JSON output to
// api.Issue. The binary name defaults to "jira" and can be overridden via
// the PGPR_JIRA_BINARY environment variable (no ZR strings here — the ZR
// tenant sets that env via its own config).
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
)

// Runner execs a command and returns its stdout. Injectable for tests.
type Runner interface {
	Run(ctx context.Context, argv []string) (stdout []byte, err error)
}

// osRunner is the production Runner: exec.CommandContext, stdout only.
type osRunner struct{}

func (osRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

// Provider implements issues.Provider by shelling out to the generic jira CLI.
type Provider struct {
	binary string
	runner Runner
}

// New constructs the default Provider. The binary name is taken from
// PGPR_JIRA_BINARY if set, otherwise "jira".
func New() *Provider {
	bin := os.Getenv("PGPR_JIRA_BINARY")
	if bin == "" {
		bin = "jira"
	}
	return &Provider{binary: bin, runner: osRunner{}}
}

// NewWithRunner constructs a Provider with an injectable runner (for tests).
func NewWithRunner(binary string, runner Runner) *Provider {
	return &Provider{binary: binary, runner: runner}
}

// Binary reports the resolved binary name this provider will exec.
func (p *Provider) Binary() string { return p.binary }

// Compile-time interface check.
var _ issues.Provider = (*Provider)(nil)

// cliIssue is the JSON shape emitted by `jira issue <KEY>` (repo-base
// pkg/jira.Issue). We decode the fields needed now plus priority/labels/
// issuetype so the mapping is forward-ready for UJ-3 (pg2-4c5i.26), even
// though api.Issue currently only carries {ID,Title,State,URL}.
type cliIssue struct {
	Key       string   `json:"key"`
	Summary   string   `json:"summary"`
	Status    string   `json:"status"`
	IssueType string   `json:"issuetype"`
	Labels    []string `json:"labels"`
	URL       string   `json:"url"`
	Priority  string   `json:"priority,omitempty"`
}

// GetIssue execs `<binary> issue <key>`, decodes the JSON envelope, and maps
// cliIssue → api.Issue.
//
// Field mapping (current; UJ-3 will extend api.Issue with Priority/incident):
//
//	cliIssue.Key     → api.Issue.ID
//	cliIssue.Summary → api.Issue.Title
//	cliIssue.Status  → api.Issue.State
//	cliIssue.URL     → api.Issue.URL
func (p *Provider) GetIssue(ctx context.Context, id string) (*api.Issue, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("jira provider: empty issue id")
	}
	stdout, err := p.runner.Run(ctx, []string{p.binary, "issue", id})
	if err != nil {
		return nil, fmt.Errorf("jira provider: exec %q issue %s: %w", p.binary, id, err)
	}
	var raw cliIssue
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("jira provider: decode output for %s: %w", id, err)
	}
	if raw.Key == "" {
		return nil, fmt.Errorf("jira provider: response for %s is missing key field", id)
	}
	return &api.Issue{
		ID:    raw.Key,
		Title: raw.Summary,
		State: raw.Status,
		URL:   raw.URL,
	}, nil
}
