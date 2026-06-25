package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// GitHubIssues / JiraIssues are external issue-tracker work sources. Each shells out
// to its tracker's CLI through Env.Cmd (the same seam CommandQuery uses, so they stay
// unit-testable with a fake Commander) and maps the result to []item.Item.
// Authentication is the CLI's own (gh's GH_TOKEN/`gh auth`; the jira CLI's config) —
// pr-pool adds no credential handling, exactly as pg-pr's providers delegate to `gh`.

// issueListLimit bounds how many issues a single tracker query returns. pr-pool only
// drains up to a role's Cap per pass, so this need only comfortably exceed any one
// pass's cap; Run logs when the limit is hit so a truncated backlog is never mistaken
// for the whole queue (no silent caps).
const issueListLimit = 200

// --- github-issues ---

// GitHubIssues lists OPEN issues in Repo via `gh issue list`, optionally narrowed to
// issues carrying ALL of Labels (gh treats repeated --label as AND).
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

// ghIssue is the subset of `gh issue list --json` fields mapped to an Item.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (q GitHubIssues) Run(ctx context.Context, env Env) ([]item.Item, error) {
	argv := []string{
		"gh", "issue", "list",
		"--repo", q.Repo,
		"--state", "open",
		"--limit", strconv.Itoa(issueListLimit),
		"--json", "number,title,url,labels",
	}
	for _, l := range q.Labels {
		argv = append(argv, "--label", l)
	}
	out, err := commander(env).Run(ctx, argv)
	if err != nil {
		return nil, fmt.Errorf("github-issues query %s: %w", q.Repo, err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil // no issues: gh prints nothing / [] depending on version
	}
	var raw []ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("github-issues query %s: parse gh issue list JSON: %w", q.Repo, err)
	}
	items := make([]item.Item, 0, len(raw))
	for _, gi := range raw {
		labels := make([]string, 0, len(gi.Labels))
		for _, l := range gi.Labels {
			labels = append(labels, l.Name)
		}
		items = append(items, item.Item{
			ID:    fmt.Sprintf("%s#%d", q.Repo, gi.Number),
			Type:  "github-issue",
			Title: gi.Title,
			Metadata: map[string]any{
				"repo":   q.Repo,
				"number": gi.Number,
				"url":    gi.URL,
				"labels": labels,
			},
		})
	}
	warnIfTruncated("github-issues", q.Repo, len(items))
	return items, nil
}

// --- jira-issues ---

// JiraIssues lists unresolved issues via the `jira` CLI (ankitpokhrel/jira-cli),
// requesting the raw Jira REST search response with `--raw`. JQL takes precedence
// when set; otherwise a default JQL is built from Project (+ Labels). The CLI's own
// config supplies the tenant URL and credentials.
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

// jql returns the explicit JQL when set, else a default that selects unresolved
// issues in Project narrowed to ALL of Labels. Values are passed as a single argv
// element (no shell), so quoting only needs to be valid JQL, not shell-safe.
func (q JiraIssues) jql() string {
	if strings.TrimSpace(q.JQL) != "" {
		return q.JQL
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "project = %q", q.Project)
	for _, l := range q.Labels {
		fmt.Fprintf(&sb, " AND labels = %q", l)
	}
	sb.WriteString(" AND resolution = Unresolved ORDER BY created ASC")
	return sb.String()
}

// jiraSearchResult is the subset of the Jira REST /search response (emitted verbatim
// by `jira issue list --raw`) mapped to Items.
type jiraSearchResult struct {
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Summary   string   `json:"summary"`
			Labels    []string `json:"labels"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	} `json:"issues"`
}

func (q JiraIssues) Run(ctx context.Context, env Env) ([]item.Item, error) {
	argv := []string{
		"jira", "issue", "list",
		"--jql", q.jql(),
		"--paginate", "0:" + strconv.Itoa(issueListLimit),
		"--raw",
	}
	out, err := commander(env).Run(ctx, argv)
	if err != nil {
		return nil, fmt.Errorf("jira-issues query: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var res jiraSearchResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("jira-issues query: parse jira issue list JSON: %w", err)
	}
	items := make([]item.Item, 0, len(res.Issues))
	for _, ji := range res.Issues {
		if ji.Key == "" {
			return nil, fmt.Errorf("jira-issues query: issue missing required \"key\"")
		}
		items = append(items, item.Item{
			ID:    ji.Key,
			Type:  "jira-issue",
			Title: ji.Fields.Summary,
			Metadata: map[string]any{
				"project":   q.Project,
				"key":       ji.Key,
				"issuetype": ji.Fields.IssueType.Name,
				"status":    ji.Fields.Status.Name,
				"labels":    ji.Fields.Labels,
			},
		})
	}
	warnIfTruncated("jira-issues", q.Project, len(items))
	return items, nil
}

// --- shared helpers ---

// commander returns the Env's Commander, defaulting to the os/exec one (mirrors
// CommandQuery so a nil Env.Cmd still works in production).
func commander(env Env) Commander {
	if env.Cmd != nil {
		return env.Cmd
	}
	return OSCommander{}
}

// warnIfTruncated logs when a tracker query returned exactly issueListLimit items —
// the backlog may extend past the limit, so surface it rather than silently capping.
func warnIfTruncated(kind, source string, n int) {
	if n >= issueListLimit {
		slog.Warn("issue query hit the result limit; backlog may be truncated",
			"query", kind, "source", source, "limit", issueListLimit)
	}
}

// IsStub reports whether a query type is a not-yet-implemented stub. No query types
// are stubs currently (github-issues and jira-issues are implemented above); the
// seam is retained so the drain pre-flight can keep warning if a future type lands
// as a decode/validate-only stub.
func IsStub(Query) bool { return false }
