package query

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// GitHubIssues / JiraIssues are decode/validate stubs (spec C scope): they
// establish the union shape; Run is not yet implemented. Follow-up: see spec C
// "Out of scope / deferred".
type GitHubIssues struct {
	Repo   string   `toml:"repo"`
	Labels []string `toml:"labels"`
}

func (q GitHubIssues) Validate() error {
	if q.Repo == "" {
		return fmt.Errorf("github-issues query: repo is required")
	}
	return nil
}
func (q GitHubIssues) Run(context.Context, Env) ([]item.Item, error) {
	return nil, fmt.Errorf("github-issues query not yet implemented (spec C deferred)")
}

type JiraIssues struct {
	Project string   `toml:"project"`
	JQL     string   `toml:"jql"`
	Labels  []string `toml:"labels"`
}

func (q JiraIssues) Validate() error {
	if q.Project == "" && q.JQL == "" {
		return fmt.Errorf("jira-issues query: project or jql is required")
	}
	return nil
}
func (q JiraIssues) Run(context.Context, Env) ([]item.Item, error) {
	return nil, fmt.Errorf("jira-issues query not yet implemented (spec C deferred)")
}

// IsStub reports whether a query type is a not-yet-implemented stub (pre-flight
// warns on these while Validate still passes).
func IsStub(q Query) bool {
	switch q.(type) {
	case GitHubIssues, JiraIssues:
		return true
	}
	return false
}
