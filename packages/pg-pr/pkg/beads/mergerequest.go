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
	"sort"
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
	// Dependencies are the bead's OUTGOING dependency edges, carried so the
	// duplicate audit can read the `supersedes` adjudication marker out of the
	// same `bd list` it already issues (adjudication.go). Not serialized: the
	// audit's --json shape is a consumed contract and adjudicated beads never
	// appear in it.
	Dependencies []Dependency `json:"-"`
}

// bdIssue is the bd CLI's JSON shape (subset we care about). Metadata
// values are strings or numbers depending on what was set, so we decode
// into a generic map and convert as needed.
type bdIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Type        string `json:"issue_type"`
	Priority    int    `json:"priority"`
	Description string `json:"description,omitempty"`
	// CreatedAt is bd's creation timestamp (RFC3339). Used to order duplicates
	// and to pick the newest closed process-feedback predecessor.
	CreatedAt    string         `json:"created_at,omitempty"`
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
// diff — MUST call getMergeRequestUncached directly (or, from outside this
// package, GetMergeRequestUncached), NOT this method, so a stale snapshot can
// never corrupt their decision.
//
// KEEP decision (pg2-dyu43): as of this audit it has zero production callers
// in pg-pr — `grep -rn "\.GetMergeRequest(" --include=*.go packages/pg-pr | grep -v
// _test.go` returns nothing. Production identity/existence reads by
// (repo, pr_number) go through FindByRepoAndNumber instead, since the
// pg2-pz7y8 read-once/write-once refactor moved the beadsbridge.BeadClient
// interface off of by-id reads entirely (it no longer declares GetMergeRequest
// or GetMergeRequestUncached at all — only FindByRepoAndNumberUncached and
// FindByRepoAndNumber). This method is retained deliberately, not left
// ambiguous: it lives in pkg/ (this module's intentionally-exported surface,
// as distinct from internal/), has correct and fully documented cache+fallback
// semantics, and is exercised directly by this package's own tests
// (mergerequest_test.go, tickcache_writepath_test.go). Retire it only
// alongside removing that test coverage, if a future audit decides the
// exported by-id surface is no longer worth carrying.
func (c *Client) GetMergeRequest(ctx context.Context, id string) (*MergeRequest, error) {
	if c.tickCache != nil {
		if mr, ok := c.tickCache.MergeRequestsByID[id]; ok {
			cached := mr
			return &cached, nil
		}
	}
	return c.getMergeRequestUncached(ctx, id)
}

// GetMergeRequestUncached is the exported form of getMergeRequestUncached, for
// out-of-package STATE-DECISION callers that must not read through the per-tick
// cache. It exists so an FB-3-style threaded read (one fetch serving several
// per-tick reconcilers) can still be provably fresh: the beadsbridge prefetches
// with this, then hands the result to SetMergeRequestCoOwnedWith's FB-4 diff and
// to reconcilePriority. Using GetMergeRequest there instead would make the
// freshness of a diff-before-write depend on whether a cache happens to be
// attached to that client — the coupling FB-5's field doc warns against.
func (c *Client) GetMergeRequestUncached(ctx context.Context, id string) (*MergeRequest, error) {
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

// EnsureMergeRequest is the idempotent upsert used by the sync engine. At most
// ONE merge-request bead may exist per (repo, pr_number) (pg2-onq1e): a
// re-sync UPDATES the canonical bead and never creates a second one.
//
//   - If a bead with matching repo + pr_number exists and is open, fields
//     are merged in via UpdateMergeRequest.
//   - If such a bead exists but is closed, the bead is NOT reopened; the
//     returned (id, alreadyClosed=true) lets callers skip.
//   - If no matching bead exists, a new one is created via
//     CreateMergeRequest.
//
// When the workspace ALREADY holds duplicates for the pair, findByRepoPR
// collapses onto the canonical one (pickCanonicalMergeRequest) so the updates
// stop alternating between them. Collapsing the duplicates themselves is
// deliberately NOT done here — see FindDuplicateMergeRequests.
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
//
// It reads the bead once (for the diff-before-write skip) and delegates to
// SetMergeRequestCoOwnedWith. A caller that has ALREADY fetched the bead this
// tick should call SetMergeRequestCoOwnedWith directly to avoid the redundant
// read (FB-3).
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
	var prefetched *MergeRequest
	if mr, err := c.getMergeRequestUncached(ctx, id); err == nil {
		prefetched = mr
	}
	return c.SetMergeRequestCoOwnedWith(ctx, id, coOwned, prefetched)
}

// SetMergeRequestCoOwnedWith is SetMergeRequestCoOwned given an ALREADY-fetched
// merge-request bead, letting the caller thread one GetMergeRequest read across
// several per-tick reconcilers instead of each re-reading it (FB-3 — cut Dolt
// connection churn on the hot pr.updated path).
//
// prefetched carries the current label set for the diff-before-write skip. A
// nil prefetched (bead unknown / read failed / not found) falls through to the
// write — identical to SetMergeRequestCoOwned's behavior when it cannot
// POSITIVELY read the current labels. The bd command emitted and the skip
// predicate are otherwise unchanged from SetMergeRequestCoOwned; the only
// difference is where the bead read comes from.
//
// prefetched MUST come from an UNCACHED read (GetMergeRequestUncached, or
// getMergeRequestUncached in-package) — NEVER from GetMergeRequest, whose
// per-tick TickCache snapshot may predate a write issued earlier in the same
// tick. This is the FB-4 diff, and FB-5 excluded the diff-before-write paths
// from the cache for exactly this reason: a stale label set would skip a
// genuinely needed write. Threading the read (FB-3) moves WHERE the read
// happens, never WHETHER it is fresh.
func (c *Client) SetMergeRequestCoOwnedWith(ctx context.Context, id string, coOwned bool, prefetched *MergeRequest) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	if prefetched != nil && hasLabel(prefetched.Labels, coOwnedLabel) == coOwned {
		return nil
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
	_, err := c.Runner.Run(ctx, "update", id, "-p", strconv.Itoa(clampMergeRequestPriority(p)))
	return err
}

// clampMergeRequestPriority clamps p into bd's valid [0,4] priority range.
func clampMergeRequestPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 4 {
		return 4
	}
	return p
}

// bdDefaultPriority is bd's own documented default priority for a bead
// created with no explicit `-p` (`bd create --help`: "-p, --priority string
// ... (default \"2\")"). ReconcileMergeRequest uses it to seed the
// priority/pbase baseline when no bead exists yet, reproducing exactly what a
// follow-up read of a freshly created bead would show — without spending
// that extra read.
const bdDefaultPriority = 2

// pbaseLabelPrefix stashes the pre-conflict-adjustment priority on a
// merge-request bead so a repeated conflicting tick is a no-op and a clear
// restores the exact baseline (pg2-tsgkj). Relocated here from
// internal/beadsbridge/bridge.go (pg2-pz7y8): the conflict-priority decision
// now runs inside the read-once/write-once ReconcileMergeRequest below, so it
// lives in the same package as the diff it feeds.
const pbaseLabelPrefix = "pbase:"

// mergeRequestPriorityDelta is the pure decision half of the former
// internal/beadsbridge/bridge.go reconcilePriority: given the bead's CURRENT
// priority/labels, whether it actsAsMine (mine or co-owned; team otherwise),
// and whether the PR currently has a conflict, it returns the label/priority
// mutations needed — without issuing any bd call. mine/co-owned raise (toward
// 0, clamp 0); team lowers (toward 4, clamp 4). Semantics are copied verbatim
// from the original reconcilePriority so this is a pure relocation, not a
// behavior change.
func mergeRequestPriorityDelta(curPriority int, curLabels []string, actsAsMine, hasConflict bool) (addLabels, removeLabels []string, priority int, setPriority bool) {
	baseline, hasBaseline := parsePbase(curLabels)
	switch {
	case hasConflict && !hasBaseline:
		// First conflicting tick: stash current priority, then nudge.
		// Stash unconditionally — even when priority is already clamped at the
		// boundary (desired == curPriority) — so a later clear is a
		// no-op-safe restore.
		addLabels = []string{pbaseLabelPrefix + strconv.Itoa(curPriority)}
		desired := nudgedPriority(curPriority, actsAsMine)
		if desired != curPriority {
			return addLabels, nil, desired, true
		}
		return addLabels, nil, 0, false
	case hasConflict && hasBaseline:
		return nil, nil, 0, false // already adjusted this conflict episode — idempotent no-op
	case !hasConflict && hasBaseline:
		// Conflict cleared: restore baseline, drop the marker.
		removeLabels = []string{pbaseLabelPrefix + strconv.Itoa(baseline)}
		if curPriority != baseline {
			return nil, removeLabels, baseline, true
		}
		return nil, removeLabels, 0, false
	default:
		return nil, nil, 0, false // no conflict, no baseline — nothing to do
	}
}

// nudgedPriority returns the conflict-adjusted priority: mine/co-owned raise
// (toward 0), team lowers (toward 4). Clamped to [0,4].
func nudgedPriority(p int, actsAsMine bool) int {
	if actsAsMine {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// parsePbase extracts the stashed baseline priority from a `pbase:<n>` label.
func parsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if strings.HasPrefix(l, pbaseLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, pbaseLabelPrefix)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// FindByRepoAndNumberUncached is the exported, always-fresh state read for a
// merge-request bead by (repo, pr_number), bypassing any attached per-tick
// cache. Unlike FindByRepoAndNumber (an IDENTITY/EXISTENCE lookup that MAY be
// served from a per-tick cache when one is attached), this is for
// correctness-critical callers that need fresh field/label/priority values —
// principally ReconcileMergeRequest's caller, which reads exactly once per
// flush before deciding any diff. Mirrors GetMergeRequestUncached's rationale
// for the by-id case (pg2-pz7y8).
func (c *Client) FindByRepoAndNumberUncached(ctx context.Context, repo string, prNumber int) (*MergeRequest, error) {
	if repo == "" || prNumber <= 0 {
		return nil, errors.New("merge-request: repo and pr_number required")
	}
	return c.findByRepoPR(ctx, repo, prNumber)
}

// ReconcileMergeRequest is the read-once + single-write projection for one
// PR's merge-request bead (pg2-pz7y8's target shape: "read once to get the
// current state, do all the work, then one create/update"). existing MUST
// come from ONE fresh (uncached) read taken by the caller immediately before
// this call — FindByRepoAndNumberUncached — so every diff below compares
// against fresh state, preserving the FB-1/2/4 diff-before-write invariant;
// ReconcileMergeRequest itself performs NO read.
//
// It replaces the former EnsureMergeRequest → GetMergeRequestUncached →
// SetMergeRequestCoOwnedWith → (AddLabel/RemoveLabel/SetPriority via
// reconcilePriority) chain — up to 2 reads and up to 4 separate bd calls on a
// single tick — with the one read the caller already did plus AT MOST ONE
// combined bd create-or-update call carrying every desired mutation (metadata
// fields, the co-owned label, and the conflict-priority/pbase label). When
// nothing needs to change, it issues ZERO bd calls.
//
// existing == nil creates a new bead. An existing CLOSED bead is returned
// as-is (alreadyClosed=true) with NO writes at all — never reopened, never
// diffed — exactly like EnsureMergeRequest's contract.
//
// coOwned is the desired co-owned label state; hasConflict and actsAsMine
// drive the conflict-priority nudge exactly as the former reconcilePriority
// did (see mergeRequestPriorityDelta).
func (c *Client) ReconcileMergeRequest(
	ctx context.Context,
	existing *MergeRequest,
	userTitle string,
	fields MergeRequestFields,
	coOwned bool,
	hasConflict bool,
	actsAsMine bool,
) (id string, alreadyClosed bool, err error) {
	if fields.Repo == "" || fields.PRNumber == 0 {
		return "", false, errors.New("merge-request: repo and pr_number required")
	}
	if existing != nil && existing.Status == "closed" {
		return existing.ID, true, nil
	}

	// curPriority/curLabels model the bead's CURRENT state for the co-owned
	// and priority/pbase diffs — either the just-read existing bead, or bd's
	// own documented creation defaults (priority 2, no labels) when none
	// exists yet. Using bd's default here is not a new assumption: it is
	// exactly what a follow-up read of a freshly created bead would show, so
	// this reproduces the pre-refactor observable behavior without spending
	// that read.
	curPriority := bdDefaultPriority
	var curLabels []string
	if existing != nil {
		curPriority = existing.Priority
		curLabels = existing.Labels
	}

	addLabels, removeLabels, priority, setPriority := mergeRequestPriorityDelta(curPriority, curLabels, actsAsMine, hasConflict)
	if hasLabel(curLabels, coOwnedLabel) != coOwned {
		if coOwned {
			addLabels = append(addLabels, coOwnedLabel)
		} else {
			removeLabels = append(removeLabels, coOwnedLabel)
		}
	}

	if existing == nil {
		newID, cerr := c.createMergeRequestCombined(ctx, userTitle, fields, addLabels, priority, setPriority)
		return newID, false, cerr
	}

	needsFieldWrite := !metadataUnchanged(existing.Fields, fields)
	if !needsFieldWrite && len(addLabels) == 0 && len(removeLabels) == 0 && !setPriority {
		return existing.ID, false, nil // nothing changed: zero bd calls
	}
	if err := c.updateMergeRequestCombined(ctx, existing.ID, fields, needsFieldWrite, addLabels, removeLabels, priority, setPriority); err != nil {
		return existing.ID, false, err
	}
	return existing.ID, false, nil
}

// createMergeRequestCombined issues the single `bd create` call for a new
// merge-request bead, carrying metadata fields plus any labels/priority
// ReconcileMergeRequest decided are needed on day one (e.g. a PR that already
// has a conflict, or is already co-owned, on its very first sync tick).
func (c *Client) createMergeRequestCombined(ctx context.Context, userTitle string, fields MergeRequestFields, addLabels []string, priority int, setPriority bool) (string, error) {
	if fields.LastSyncedAt == "" {
		fields.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
	}
	title := userTitle
	if title == "" {
		title = fmt.Sprintf("%s#%d", fields.Repo, fields.PRNumber)
	} else {
		title = fmt.Sprintf("%s#%d: %s", fields.Repo, fields.PRNumber, title)
	}
	metaJSON, err := encodeMetadata(fields)
	if err != nil {
		return "", err
	}
	args := []string{
		"create",
		"--type=merge-request",
		"--title", title,
		"-d", title,
		"--metadata", metaJSON,
		"--silent",
	}
	if len(addLabels) > 0 {
		args = append(args, "-l", strings.Join(addLabels, ","))
	}
	if setPriority {
		args = append(args, "-p", strconv.Itoa(clampMergeRequestPriority(priority)))
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

// updateMergeRequestCombined issues the single `bd update` call carrying
// every mutation ReconcileMergeRequest decided is needed: the metadata patch
// (when fields differ), label adds/removes (co-owned, pbase), and a priority
// change — combined so an update-worthy tick spends exactly one bd call
// regardless of how many of the three changed.
func (c *Client) updateMergeRequestCombined(ctx context.Context, id string, fields MergeRequestFields, needsFieldWrite bool, addLabels, removeLabels []string, priority int, setPriority bool) error {
	args := []string{"update", id}
	if needsFieldWrite {
		metaJSON, err := encodeMetadata(fields)
		if err != nil {
			return err
		}
		args = append(args, "--metadata", metaJSON)
	}
	for _, l := range addLabels {
		args = append(args, "--add-label", l)
	}
	for _, l := range removeLabels {
		args = append(args, "--remove-label", l)
	}
	if setPriority {
		args = append(args, "-p", strconv.Itoa(clampMergeRequestPriority(priority)))
	}
	_, err := c.Runner.Run(ctx, args...)
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

// findByRepoPR finds THE canonical merge-request bead for repo + pr_number with
// a fresh full scan (never the per-tick cache). Returns nil if not found.
// Includes closed beads.
//
// This is the UNCACHED path on purpose: EnsureMergeRequest's FB-2
// diff-before-write compares the returned bead's STORED fields against desired,
// and must therefore read current state, not a tick-start snapshot.
//
// (repo, pr_number) is supposed to identify EXACTLY ONE bead, but the live
// workspace holds pairs where it does not (pg2-onq1e), so the pick must be
// deterministic — see pickCanonicalMergeRequest. Returning "whichever matched
// first" let bd's row order decide which bead got the field updates, which in
// turn moved where children were parented from tick to tick.
func (c *Client) findByRepoPR(ctx context.Context, repo string, pr int) (*MergeRequest, error) {
	matches, err := c.findAllByRepoPR(ctx, repo, pr)
	if err != nil {
		return nil, err
	}
	return pickCanonicalMergeRequest(matches), nil
}

// findAllByRepoPR returns EVERY merge-request bead whose metadata matches
// repo + pr_number, open or closed, in bd's row order. More than one is the
// duplication defect; the audit path reports them and the write paths collapse
// onto the canonical one.
func (c *Client) findAllByRepoPR(ctx context.Context, repo string, pr int) ([]MergeRequest, error) {
	all, err := c.ListMergeRequests(ctx, true)
	if err != nil {
		return nil, err
	}
	var out []MergeRequest
	for i := range all {
		if all[i].Fields.Repo == repo && all[i].Fields.PRNumber == pr {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// pickCanonicalMergeRequest chooses the single bead that owns a (repo,
// pr_number) pair when several exist. Precedence, most significant first:
//
//  1. OPEN over closed — a closed bead must not capture a live PR's updates.
//  2. The most recently synced (last_synced_at) — that is the bead the daemon
//     has actually been maintaining and the one children already hang off, so
//     the choice is STABLE: the winner keeps being the winner every later tick.
//  3. Lexicographically smallest ID — a total order, so the pick never depends
//     on bd's row order.
//
// Returns nil for an empty slice.
func pickCanonicalMergeRequest(matches []MergeRequest) *MergeRequest {
	var best *MergeRequest
	for i := range matches {
		cand := &matches[i]
		if best == nil || mergeRequestMoreCanonical(cand, best) {
			best = cand
		}
	}
	return best
}

// mergeRequestMoreCanonical reports whether a outranks b under
// pickCanonicalMergeRequest's precedence.
func mergeRequestMoreCanonical(a, b *MergeRequest) bool {
	aOpen, bOpen := a.Status != "closed", b.Status != "closed"
	if aOpen != bOpen {
		return aOpen
	}
	if a.Fields.LastSyncedAt != b.Fields.LastSyncedAt {
		return a.Fields.LastSyncedAt > b.Fields.LastSyncedAt
	}
	return a.ID < b.ID
}

// DuplicateMergeRequests reports one (repo, pr_number) pair that resolves to
// more than one merge-request bead: Canonical is the bead the write paths use,
// Excess is every other bead for the same pair.
type DuplicateMergeRequests struct {
	Repo      string         `json:"repo"`
	PRNumber  int            `json:"pr_number"`
	Canonical MergeRequest   `json:"canonical"`
	Excess    []MergeRequest `json:"excess"`
}

// FindDuplicateMergeRequests scans every merge-request bead (open and closed)
// and reports each (repo, pr_number) pair that resolves to more than one. It is
// READ-ONLY — one `bd list` and no writes — and exists so an operator can see
// the excess beads before deciding what to do about them. It MUST NOT be given
// a mutating counterpart in this package: collapsing existing beads is an
// operator-scheduled data migration, not something a sync tick may do.
//
// ADJUDICATED duplicates are excluded (pg2-peyf0): a bead recorded as resolving
// into a canonical bead for the SAME pair, via a `supersedes` dependency edge, is
// retired from its group before the count, so a completed reconcile actually
// moves the number. A bead that is merely CLOSED is not adjudicated and is still
// counted — closing a duplicate does not resolve it (pg2-0z8fw). The edges come
// out of the same `bd list` this already issues, so the exclusion costs no extra
// bd call and the audit stays one read.
//
// Pairs are returned sorted by repo then PR number so the report is stable.
func (c *Client) FindDuplicateMergeRequests(ctx context.Context) ([]DuplicateMergeRequests, error) {
	all, err := c.ListMergeRequests(ctx, true)
	if err != nil {
		return nil, err
	}
	type key struct {
		repo string
		pr   int
	}
	groups := map[key][]MergeRequest{}
	var order []key
	for i := range all {
		f := all[i].Fields
		if f.Repo == "" || f.PRNumber == 0 {
			continue // not keyed on a PR; not a duplicate candidate
		}
		k := key{f.Repo, f.PRNumber}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], all[i])
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].repo != order[j].repo {
			return order[i].repo < order[j].repo
		}
		return order[i].pr < order[j].pr
	})
	var out []DuplicateMergeRequests
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		g = dropAdjudicated(g,
			func(m MergeRequest) string { return m.ID },
			func(m MergeRequest) []Dependency { return m.Dependencies },
			pickCanonicalMergeRequest)
		if len(g) < 2 {
			continue // every duplicate for this pair has been adjudicated away
		}
		canonical := pickCanonicalMergeRequest(g)
		dup := DuplicateMergeRequests{Repo: k.repo, PRNumber: k.pr, Canonical: *canonical}
		for i := range g {
			if g[i].ID != canonical.ID {
				dup.Excess = append(dup.Excess, g[i])
			}
		}
		sort.Slice(dup.Excess, func(i, j int) bool { return dup.Excess[i].ID < dup.Excess[j].ID })
		out = append(out, dup)
	}
	return out, nil
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
// WOULD set (encodeMetadata omits zero values for the string/int fields, but
// emits draft unconditionally — see encodeMetadata) already holds that value
// in stored. It mirrors encodeMetadata's omit semantics exactly, so it is true
// precisely when `bd update --metadata` would change nothing.
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
	// draft is compared for EQUALITY, not one-directionally: encodeMetadata
	// now emits an explicit draft (true or false) unconditionally
	// (pg2-4dz88.10 — previously it only ever SET draft=true and never
	// cleared it, so a stored true could never be brought back to false
	// through this mechanism). A genuine draft<->ready transition in EITHER
	// direction must therefore be treated as a change.
	if desired.Draft != stored.Draft {
		return false
	}
	// last_synced_at excluded by design (FB-1).
	return true
}

// encodeMetadata serializes f as a JSON object that bd's --metadata flag
// accepts. Most fields are omitted when zero-valued (so a partial patch
// doesn't clobber fields the caller didn't set); draft is the one exception,
// always emitted explicitly — see its assignment below.
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
	// draft is emitted UNCONDITIONALLY (both true and false), unlike the
	// string/int fields above which omit their zero value: a bool has no
	// "unset" representation, and desired.Draft==false (not-a-draft) is a
	// meaningful state that must be able to clear a previously-stored true
	// through bd's metadata-merge semantics (pg2-4dz88.10). Every caller
	// passes the PR's current, fully-known Draft state — never a value it
	// means to leave untouched — so this is safe for the "re-assert the full
	// field set every tick" design EnsureMergeRequest documents.
	m["draft"] = f.Draft
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
		ID:           iss.ID,
		Title:        iss.Title,
		Status:       iss.Status,
		Type:         iss.Type,
		Fields:       f,
		Priority:     iss.Priority,
		Labels:       iss.Labels,
		Dependencies: dependenciesFromBD(iss.Dependencies),
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
