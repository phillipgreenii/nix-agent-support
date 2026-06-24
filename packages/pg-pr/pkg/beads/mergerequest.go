// Package beads — merge-request bead wrappers.
//
// The wrappers shell out to `bd` and parse `--json` output. The exec layer is
// injectable via Runner so unit tests can drive the wrappers without an
// actual bd workspace; integration tests use the real CLIRunner against a
// disposable `bd init --reinit-local --prefix=tN` workspace.
package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MergeRequestFields is the subset of bead metadata pg-pr stores on each
// merge-request bead. The struct mirrors the keys documented in
// docs/superpowers/specs/2026-05-19-pg-pr-design.md.
type MergeRequestFields struct {
	Repo           string `json:"repo"`
	PRNumber       int    `json:"pr_number"`
	State          string `json:"state,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Base           string `json:"base,omitempty"`
	Author         string `json:"author,omitempty"`
	URL            string `json:"url,omitempty"`
	LastSyncedAt   string `json:"last_synced_at,omitempty"`
	SyncError      string `json:"sync_error,omitempty"`
	CIOnlyAttempts int    `json:"ci_only_attempts,omitempty"`
	Draft          bool   `json:"draft,omitempty"`
}

// CreateMergeRequestInput is the typed input for creating a merge-request bead.
type CreateMergeRequestInput struct {
	Title  string
	Fields MergeRequestFields
}

// MergeRequest is a parsed view of a merge-request bead returned by bd.
type MergeRequest struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Status string             `json:"status"`
	Type   string             `json:"issue_type"`
	Fields MergeRequestFields `json:"-"`
}

// bdIssue is the bd CLI's JSON shape (subset we care about). Metadata
// values are strings or numbers depending on what was set, so we decode
// into a generic map and convert as needed.
type bdIssue struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Status       string         `json:"status"`
	Type         string         `json:"issue_type"`
	Metadata     map[string]any `json:"metadata"`
	Dependencies []bdDependency `json:"dependencies,omitempty"`
}

// bdDependency mirrors one entry of bd's `dependencies` field in
// `bd list --json`. issue_id is the dependent (the issue carrying the
// edge); depends_on_id is the bead the edge points at.
type bdDependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// Client is a stateful wrapper holding a Runner. Use NewClient to construct.
type Client struct {
	Runner Runner
}

// NewClient returns a Client backed by the default CLIRunner.
//
// The default Client invokes bd from the process's current working directory,
// so bd discovers its workspace from cwd. For pg-pr operations on PRs from a
// specific monorepo, prefer NewClientForRepo so bd hits that monorepo's
// `.beads/` workspace regardless of where pg-pr was invoked from.
func NewClient() *Client {
	return &Client{Runner: NewCLIRunner()}
}

// NewClientForRepo returns a Client whose underlying CLIRunner.Dir is set to
// the given absolute monorepo root. bd shells out with that path as cwd, so
// it discovers the monorepo's `.beads/` workspace (and any associated dolt
// server configuration) automatically.
//
// Use this when pg-pr is performing a write/read for a PR that belongs to a
// known monorepo: pass the absolute path from `config.RepoConfig.Path` (or
// `branch.Detect`'s WorktreeRoot) so the operation lands in the right
// workspace. Passing an empty dir is equivalent to NewClient() and uses the
// process cwd's workspace.
func NewClientForRepo(dir string) *Client {
	return NewClientWithRunner(NewCLIRunnerForRepo(dir))
}

// NewClientWithRunner returns a Client backed by an injected Runner — used in
// tests and to point at a specific bd workspace via CLIRunner.Dir.
func NewClientWithRunner(r Runner) *Client {
	return &Client{Runner: r}
}

// CreateMergeRequest creates a new merge-request bead. Returns the bead ID.
func (c *Client) CreateMergeRequest(ctx context.Context, in CreateMergeRequestInput) (string, error) {
	if in.Title == "" {
		return "", errors.New("merge-request: title required")
	}
	if in.Fields.Repo == "" || in.Fields.PRNumber == 0 {
		return "", errors.New("merge-request: repo and pr_number required")
	}
	metaJSON, err := encodeMetadata(in.Fields)
	if err != nil {
		return "", err
	}
	args := []string{
		"create",
		"--type=merge-request",
		"--title", in.Title,
		"-d", in.Title,
		"--metadata", metaJSON,
		"--silent",
	}
	out, err := c.Runner.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("bd create returned empty ID")
	}
	return id, nil
}

// UpdateMergeRequest patches metadata on an existing merge-request bead.
// Field values are merged into the existing metadata map (bd's default
// behavior on `--metadata`); zero values are omitted from the patch.
func (c *Client) UpdateMergeRequest(ctx context.Context, id string, fields MergeRequestFields) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	metaJSON, err := encodeMetadata(fields)
	if err != nil {
		return err
	}
	_, err = c.Runner.Run(ctx, "update", id, "--metadata", metaJSON)
	return err
}

// CloseMergeRequest closes a merge-request bead with the given reason.
// Idempotent: closing an already-closed bead is a no-op.
func (c *Client) CloseMergeRequest(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	mr, err := c.GetMergeRequest(ctx, id)
	if err != nil {
		return err
	}
	if mr != nil && mr.Status == "closed" {
		return nil
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err = c.Runner.Run(ctx, args...)
	return err
}

// GetMergeRequest returns a single merge-request bead by ID, or nil if not
// found. Uses `bd list --id=<id> --all` since `bd show --json` is not as
// reliably structured for the subset we need.
func (c *Client) GetMergeRequest(ctx context.Context, id string) (*MergeRequest, error) {
	out, err := c.Runner.Run(ctx, "list", "--all", "--id="+id, "--json")
	if err != nil {
		return nil, err
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	for _, iss := range issues {
		if iss.ID == id {
			mr := bdIssueToMergeRequest(iss)
			return &mr, nil
		}
	}
	return nil, nil
}

// ListMergeRequests returns all merge-request beads (open or closed if
// includeClosed). Used by sync to identify beads whose upstream PR is no
// longer in the watched set.
func (c *Client) ListMergeRequests(ctx context.Context, includeClosed bool) ([]MergeRequest, error) {
	args := []string{"list", "--type=merge-request", "--json", "--limit=0"}
	if includeClosed {
		args = append(args, "--all")
	}
	out, err := c.Runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	out2 := make([]MergeRequest, 0, len(issues))
	for _, iss := range issues {
		out2 = append(out2, bdIssueToMergeRequest(iss))
	}
	return out2, nil
}

// EnsureMergeRequest is the idempotent upsert used by the sync engine.
//
//   - If a bead with matching repo + pr_number exists and is open, fields
//     are merged in via UpdateMergeRequest.
//   - If such a bead exists but is closed, the bead is NOT reopened; the
//     returned (id, alreadyClosed=true) lets callers skip.
//   - If no matching bead exists, a new one is created via
//     CreateMergeRequest.
//
// The title rendered for new beads is "<repo>#<pr_number>: <user-title>".
func (c *Client) EnsureMergeRequest(ctx context.Context, userTitle string, fields MergeRequestFields) (id string, alreadyClosed bool, err error) {
	if fields.Repo == "" || fields.PRNumber == 0 {
		return "", false, errors.New("merge-request: repo and pr_number required")
	}
	existing, err := c.findByRepoPR(ctx, fields.Repo, fields.PRNumber)
	if err != nil {
		return "", false, err
	}
	if existing != nil {
		if existing.Status == "closed" {
			return existing.ID, true, nil
		}
		if err := c.UpdateMergeRequest(ctx, existing.ID, fields); err != nil {
			return existing.ID, false, err
		}
		return existing.ID, false, nil
	}
	title := userTitle
	if title == "" {
		title = fmt.Sprintf("%s#%d", fields.Repo, fields.PRNumber)
	} else {
		title = fmt.Sprintf("%s#%d: %s", fields.Repo, fields.PRNumber, title)
	}
	if fields.LastSyncedAt == "" {
		fields.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
	}
	id, err = c.CreateMergeRequest(ctx, CreateMergeRequestInput{
		Title:  title,
		Fields: fields,
	})
	return id, false, err
}

// findByRepoPR finds a merge-request bead by repo + pr_number metadata.
// Returns nil if not found. Includes closed beads.
func (c *Client) findByRepoPR(ctx context.Context, repo string, pr int) (*MergeRequest, error) {
	all, err := c.ListMergeRequests(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Fields.Repo == repo && all[i].Fields.PRNumber == pr {
			return &all[i], nil
		}
	}
	return nil, nil
}

// FindByRepoAndNumber finds a merge-request bead by repo + pr_number
// metadata. Returns nil if not found. Includes closed beads. Public
// wrapper around findByRepoPR for callers outside the beads package.
func (c *Client) FindByRepoAndNumber(ctx context.Context, repo string, prNumber int) (*MergeRequest, error) {
	if repo == "" || prNumber <= 0 {
		return nil, errors.New("merge-request: repo and pr_number required")
	}
	return c.findByRepoPR(ctx, repo, prNumber)
}

// encodeMetadata serializes the non-zero fields of f as a JSON object that
// bd's --metadata flag accepts.
func encodeMetadata(f MergeRequestFields) (string, error) {
	m := map[string]any{}
	if f.Repo != "" {
		m["repo"] = f.Repo
	}
	if f.PRNumber != 0 {
		m["pr_number"] = f.PRNumber
	}
	if f.State != "" {
		m["state"] = f.State
	}
	if f.Branch != "" {
		m["branch"] = f.Branch
	}
	if f.Base != "" {
		m["base"] = f.Base
	}
	if f.Author != "" {
		m["author"] = f.Author
	}
	if f.URL != "" {
		m["url"] = f.URL
	}
	if f.LastSyncedAt != "" {
		m["last_synced_at"] = f.LastSyncedAt
	}
	if f.SyncError != "" {
		m["sync_error"] = f.SyncError
	}
	if f.CIOnlyAttempts != 0 {
		m["ci_only_attempts"] = f.CIOnlyAttempts
	}
	if f.Draft {
		m["draft"] = true
	}
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode metadata: %w", err)
	}
	return string(b), nil
}

// parseBDList unmarshals the JSON output of bd list / dep list / dep tree
// / query commands. bd 1.0.4+ wraps results in an envelope:
//
//	{"data": [...], "schema_version": 1}
//
// Older bd builds returned a bare JSON array. parseBDList accepts both: it
// peeks at the first non-space byte — '{' signals the envelope, '[' signals
// the bare-array legacy shape. An empty string parses to an empty slice.
func parseBDList(s string) ([]bdIssue, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if len(s) > 0 && s[0] == '{' {
		// bd 1.0.4+ envelope: {"data":[...],"schema_version":N}
		var env struct {
			Data []bdIssue `json:"data"`
		}
		if err := json.Unmarshal([]byte(s), &env); err != nil {
			return nil, fmt.Errorf("parse bd list JSON: %w", err)
		}
		return env.Data, nil
	}
	// Legacy bare-array shape (older bd builds).
	var issues []bdIssue
	if err := json.Unmarshal([]byte(s), &issues); err != nil {
		return nil, fmt.Errorf("parse bd list JSON: %w", err)
	}
	return issues, nil
}

// bdIssueToMergeRequest converts the bd JSON shape into our typed view.
func bdIssueToMergeRequest(iss bdIssue) MergeRequest {
	f := MergeRequestFields{}
	for k, v := range iss.Metadata {
		switch k {
		case "repo":
			f.Repo = asString(v)
		case "pr_number":
			f.PRNumber = asInt(v)
		case "state":
			f.State = asString(v)
		case "branch":
			f.Branch = asString(v)
		case "base":
			f.Base = asString(v)
		case "author":
			f.Author = asString(v)
		case "url":
			f.URL = asString(v)
		case "last_synced_at":
			f.LastSyncedAt = asString(v)
		case "sync_error":
			f.SyncError = asString(v)
		case "ci_only_attempts":
			f.CIOnlyAttempts = asInt(v)
		case "draft":
			if b, ok := v.(bool); ok {
				f.Draft = b
			}
		}
	}
	return MergeRequest{
		ID:     iss.ID,
		Title:  iss.Title,
		Status: iss.Status,
		Type:   iss.Type,
		Fields: f,
	}
}

// asString tolerates both string and stringified-number values.
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// bd may serialize PR numbers as JSON numbers; we only call asString
		// for fields documented as strings, but defend anyway.
		return fmt.Sprintf("%v", x)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// asInt tolerates JSON number, string, or absent.
func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		// bd may roundtrip integer metadata as a string in some shapes.
		var n int
		if _, err := fmt.Sscanf(x, "%d", &n); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

// Package-level convenience wrappers using the default Client.

// CreateMergeRequest creates a merge-request bead using the default Client.
func CreateMergeRequest(ctx context.Context, in CreateMergeRequestInput) (string, error) {
	return NewClient().CreateMergeRequest(ctx, in)
}

// CloseMergeRequest closes a merge-request bead using the default Client.
func CloseMergeRequest(ctx context.Context, id, reason string) error {
	return NewClient().CloseMergeRequest(ctx, id, reason)
}
