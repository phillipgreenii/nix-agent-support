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
	"strconv"
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
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Status   string             `json:"status"`
	Type     string             `json:"issue_type"`
	Fields   MergeRequestFields `json:"-"`
	Priority int                `json:"-"`
	Labels   []string           `json:"-"`
}

// bdIssue is the bd CLI's JSON shape (subset we care about). Metadata
// values are strings or numbers depending on what was set, so we decode
// into a generic map and convert as needed.
type bdIssue struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Status       string         `json:"status"`
	Type         string         `json:"issue_type"`
	Priority     int            `json:"priority"`
	Labels       []string       `json:"labels,omitempty"`
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
	// tickCache, when non-nil, is a per-sync-tick snapshot consulted by the
	// IDENTITY/EXISTENCE lookups (FindByRepoAndNumber, GetMergeRequest) to
	// answer "does a bead with this repo+PR / this id exist, and what is its
	// id/status" from memory instead of a fresh full `bd list` scan. Set it
	// via UseTickCache for the lifetime of one tick.
	//
	// The cache is DELIBERATELY NOT consulted by the diff-before-write paths
	// (findByRepoPR, which EnsureMergeRequest uses for its FB-2 field diff; and
	// getMergeRequestUncached, which CloseMergeRequest's idempotency check and
	// SetMergeRequestCoOwned's FB-4 label diff use). Those compare STORED field
	// values against desired, and a tick-start snapshot can be stale relative
	// to a write issued earlier in the same tick (a backlogged/retried outbox
	// row) or an external writer on the shared workspace — feeding it into a
	// diff could skip a needed write or re-introduce the no-op commit churn
	// FB-1/FB-2 eliminated. Identity/existence is stale-tolerant; field diffs
	// are not.
	tickCache *TickCache
}

// UseTickCache attaches (or clears, with nil) a per-tick snapshot the
// IDENTITY/EXISTENCE lookups consult before shelling out to bd. Returns the
// receiver for chaining. Callers MUST only attach a cache to a Client that is
// used for identity/existence reads — never to one that also drives the
// diff-before-write paths — because those paths intentionally bypass the cache
// (see the tickCache field doc) and rely on reading fresh state. In pg-pr the
// sync engine attaches it to its per-repo read clients; the beadsbridge
// (which runs the diff-before-write projections at outbox flush) constructs
// its own SEPARATE clients and never attaches a cache, so its writes stay
// fresh.
func (c *Client) UseTickCache(cache *TickCache) *Client {
	c.tickCache = cache
	return c
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
	// Idempotency hinges on the bead's CURRENT status, so read fresh — never a
	// (possibly stale) per-tick cache snapshot.
	mr, err := c.getMergeRequestUncached(ctx, id)
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
// found. This is an IDENTITY/EXISTENCE lookup: when a per-tick cache is
// attached (UseTickCache) and holds the id, it is answered from memory with no
// bd call; a cache MISS falls back to the verbatim scan (getMergeRequestUncached)
// so a bead created/changed after the snapshot is still found.
//
// State-decision callers that need FRESH stored fields — CloseMergeRequest's
// already-closed idempotency check and SetMergeRequestCoOwned's FB-4 label
// diff — MUST call getMergeRequestUncached directly, NOT this method, so a
// stale snapshot can never corrupt their decision.
func (c *Client) GetMergeRequest(ctx context.Context, id string) (*MergeRequest, error) {
	if c.tickCache != nil {
		if mr, ok := c.tickCache.MergeRequestsByID[id]; ok {
			cached := mr
			return &cached, nil
		}
	}
	return c.getMergeRequestUncached(ctx, id)
}

// getMergeRequestUncached always shells out to bd, bypassing any attached
// per-tick cache. Uses `bd list --id=<id> --all` since `bd show --json` is not
// as reliably structured for the subset we need.
func (c *Client) getMergeRequestUncached(ctx context.Context, id string) (*MergeRequest, error) {
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
		// Diff-before-write (FB-1/FB-2): the per-minute daemon re-asserts the full
		// field set every refresh. When the stored bead already holds the desired
		// values, skip UpdateMergeRequest entirely so no `bd update` — and no Dolt
		// commit — is issued. last_synced_at is DELIBERATELY excluded from the
		// comparison (see metadataUnchanged), so a refresh whose ONLY delta is the
		// per-tick timestamp bump writes nothing: this is what eliminated the 428k
		// no-op 'nothing to commit' commits driving the Dolt journal growth.
		if metadataUnchanged(existing.Fields, fields) {
			return existing.ID, false, nil
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

// SetMergeRequestCoOwned adds (coOwned=true) or removes (false) the "co-owned"
// label on a merge-request bead — a visibility marker for a teammate PR I have
// pushed commits onto. Idempotent (bd add/remove-label are no-ops when already
// in the desired state).
func (c *Client) SetMergeRequestCoOwned(ctx context.Context, id string, coOwned bool) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	// Diff-before-write (FB-4): the daemon re-asserts the co-owned label every
	// refresh. If the label is already in the desired state, skip the `bd update`
	// (and its Dolt commit) entirely. We only skip when we can POSITIVELY read the
	// current label set; if the bead can't be read or isn't found, fall through to
	// the write rather than risk skipping a genuinely needed one. Read fresh
	// (getMergeRequestUncached) — the FB-4 diff must not compare against a stale
	// per-tick cache snapshot.
	if mr, err := c.getMergeRequestUncached(ctx, id); err == nil && mr != nil {
		if hasLabel(mr.Labels, coOwnedLabel) == coOwned {
			return nil
		}
	}
	flag := "--remove-label"
	if coOwned {
		flag = "--add-label"
	}
	_, err := c.Runner.Run(ctx, "update", id, flag, coOwnedLabel)
	return err
}

// coOwnedLabel marks a teammate PR I have pushed commits onto (a visibility
// marker synced onto the merge-request bead by SetMergeRequestCoOwned).
const coOwnedLabel = "co-owned"

// SetPriority sets the bead's priority (0=highest … 4=lowest). Used by the
// conflict-urgency reconciler (pg2-tsgkj). Out-of-range values are clamped
// into [0,4] rather than rejected.
func (c *Client) SetPriority(ctx context.Context, id string, p int) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	if p < 0 {
		p = 0
	}
	if p > 4 {
		p = 4
	}
	_, err := c.Runner.Run(ctx, "update", id, "-p", strconv.Itoa(p))
	return err
}

// AddLabel adds a label to a bead. Thin wrapper over `bd update --add-label`;
// idempotent (bd is a no-op when the label is already present). Used by the
// conflict-urgency reconciler (pg2-tsgkj) to stash the pre-adjustment priority
// in a `pbase:<n>` label.
func (c *Client) AddLabel(ctx context.Context, id, label string) error {
	_, err := c.Runner.Run(ctx, "update", id, "--add-label", label)
	return err
}

// RemoveLabel removes a label from a bead. Thin wrapper over
// `bd update --remove-label`; idempotent (bd is a no-op when the label is
// absent). Used by the conflict-urgency reconciler (pg2-tsgkj) to drop the
// `pbase:<n>` marker once the baseline priority is restored.
func (c *Client) RemoveLabel(ctx context.Context, id, label string) error {
	_, err := c.Runner.Run(ctx, "update", id, "--remove-label", label)
	return err
}

// findByRepoPR finds a merge-request bead by repo + pr_number metadata with a
// fresh full scan (never the per-tick cache). Returns nil if not found.
// Includes closed beads.
//
// This is the UNCACHED path on purpose: EnsureMergeRequest's FB-2
// diff-before-write compares the returned bead's STORED fields against desired,
// and must therefore read current state, not a tick-start snapshot.
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

// FindByRepoAndNumber finds a merge-request bead by repo + pr_number metadata.
// Returns nil if not found. Includes closed beads. Public wrapper for callers
// outside the beads package.
//
// This is an IDENTITY/EXISTENCE lookup (callers use the returned id and coarse
// open/closed status, never a field-value diff): when a per-tick cache is
// attached (UseTickCache) and holds a bead for repo+PR, it is answered from
// memory with no bd call; a cache MISS falls back to the verbatim full scan
// (findByRepoPR) so a bead created/changed after the snapshot is still found.
func (c *Client) FindByRepoAndNumber(ctx context.Context, repo string, prNumber int) (*MergeRequest, error) {
	if repo == "" || prNumber <= 0 {
		return nil, errors.New("merge-request: repo and pr_number required")
	}
	if c.tickCache != nil {
		if mr, ok := c.tickCache.FindMergeRequest(repo, prNumber); ok {
			cached := mr
			return &cached, nil
		}
	}
	return c.findByRepoPR(ctx, repo, prNumber)
}

// metadataUnchanged reports whether applying encodeMetadata(desired) as a bd
// `--metadata` patch onto stored would be a no-op — i.e. every field the patch
// WOULD set (encodeMetadata omits zero values, and draft only when true) already
// holds that value in stored. It mirrors encodeMetadata's omit semantics exactly,
// so it is true precisely when `bd update --metadata` would change nothing.
//
// last_synced_at is INTENTIONALLY excluded: the daemon bumps it every refresh,
// but nothing reads the bead's copy of it (the authoritative sync timestamp is
// the SQLite store row's last_synced_at, written independently each tick), so a
// refresh whose only delta is last_synced_at must not trigger a commit (FB-1).
// When a real field DOES change and a write is issued, the fresh last_synced_at
// rides along with that same write.
func metadataUnchanged(stored, desired MergeRequestFields) bool {
	if desired.Repo != "" && desired.Repo != stored.Repo {
		return false
	}
	if desired.PRNumber != 0 && desired.PRNumber != stored.PRNumber {
		return false
	}
	if desired.State != "" && desired.State != stored.State {
		return false
	}
	if desired.Branch != "" && desired.Branch != stored.Branch {
		return false
	}
	if desired.Base != "" && desired.Base != stored.Base {
		return false
	}
	if desired.Author != "" && desired.Author != stored.Author {
		return false
	}
	if desired.URL != "" && desired.URL != stored.URL {
		return false
	}
	if desired.SyncError != "" && desired.SyncError != stored.SyncError {
		return false
	}
	if desired.CIOnlyAttempts != 0 && desired.CIOnlyAttempts != stored.CIOnlyAttempts {
		return false
	}
	// encodeMetadata only ever SETS draft (=true); it never clears it, so a
	// desired.Draft==false can never produce a change regardless of stored.
	if desired.Draft && !stored.Draft {
		return false
	}
	// last_synced_at excluded by design (FB-1).
	return true
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
		ID:       iss.ID,
		Title:    iss.Title,
		Status:   iss.Status,
		Type:     iss.Type,
		Fields:   f,
		Priority: iss.Priority,
		Labels:   iss.Labels,
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
