// Package beads — processing-cycle bead wrappers (bd type=task).
//
// A processing-cycle bead represents one round of LLM-driven work on a PR.
// It is the parent of feedback beads created during that round; the LLM
// closes the processing-cycle when it decides the cycle is complete, and
// the sync engine watches that close to drive ci-loop escalation and
// related lifecycle work.
//
// IDENTITY (pg2-onq1e): a processing-cycle bead is keyed on (repo, pr_number),
// NOT on the merge-request bead that parents it. The key is rendered by
// ProcessingCycleKey and IS the bead's title tail, so the title carries the
// identity. Keying on the parent was the duplication bug: when a PR ended up
// with two merge-request beads, a lookup scoped to one parent could not see the
// open cycle hanging off the other, so every re-sync created another cycle. The
// parent-child edge is still wired (cascade-close depends on it) and is still
// consulted as a FALLBACK for beads created under a legacy title.
package beads

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ProcessingCycle is a parsed view of a processing-cycle bead. The json tags
// serve the read-only duplicate audit (`pg-pr sync duplicates --json`); nothing
// unmarshals INTO this type from bd (listProcessingCycles goes through bdIssue).
type ProcessingCycle struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status,omitempty"`
	ParentPRID   string `json:"parent_pr_id,omitempty"`
	ClosedReason string `json:"closed_reason,omitempty"`
	// Description is the bead body (`bd list --json` reports it verbatim).
	Description string `json:"description,omitempty"`
	// Labels carries the bead's labels — the marker channel the caller uses to
	// record WHICH feedback set this cycle already covers, so a re-sync can tell
	// "nothing new" from "new findings" without a second write. Mirrors the
	// `pbase:<n>` idiom the merge-request priority reconciler uses.
	Labels []string `json:"labels,omitempty"`
	// CreatedAt is the bead's creation timestamp, used to pick the NEWEST closed
	// predecessor deterministically.
	CreatedAt string `json:"created_at,omitempty"`
	// Dependencies are the bead's OUTGOING dependency edges, carried so the
	// duplicate audit can read the `supersedes` adjudication marker out of the
	// same `bd list` it already issues (adjudication.go). Not serialized: the
	// audit's --json shape is a consumed contract and adjudicated beads never
	// appear in it.
	Dependencies []Dependency `json:"-"`
}

// ProcessingCycleState is the resolved process-feedback bead situation for one
// PR key. At most one of the two fields is set:
//
//   - Open != nil  — a live cycle exists; the caller MUST update it, never
//     create a second one.
//   - Closed != nil — no live cycle, but a predecessor was closed; the caller
//     may only open a successor for feedback that predecessor did not cover,
//     and MUST reference it.
//
// Both nil means the PR has no process-feedback bead at all.
type ProcessingCycleState struct {
	Open   *ProcessingCycle
	Closed *ProcessingCycle
}

// processingCycleTitlePrefix is matched verbatim by the cycle lookups.
const processingCycleTitlePrefix = "process-feedback: "

// ProcessingCycleKey renders the (repo, pr_number) identity of a
// process-feedback bead. It is the title tail, so two beads with the same key
// are by definition duplicates of each other.
func ProcessingCycleKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// processingCycleTitle renders the full bead title for a key.
func processingCycleTitle(key string) string { return processingCycleTitlePrefix + key }

// CreateProcessingCycleInput is the typed input for creating a process-feedback
// bead. Mirrors CreateMergeRequestInput.
type CreateProcessingCycleInput struct {
	// PRBeadID is the merge-request bead to parent the cycle under. Required.
	PRBeadID string
	// Key is the (repo, pr_number) identity from ProcessingCycleKey. Empty
	// falls back to PRBeadID, which yields a bead the key-scoped lookup cannot
	// match — acceptable only for callers with no PR identity to hand.
	Key string
	// Description is the bead body. It MUST carry substance (what and how much
	// needs processing) so a drain session can triage without hitting the VCS
	// API; an empty value degrades to the title, which is what made the
	// duplicated beads unreadable.
	Description string
	// Mine stamps the `mine` label.
	Mine bool
	// Labels are extra labels (e.g. the feedback-set marker).
	Labels []string
}

// CreateProcessingCycle creates a new processing-cycle bead and wires a
// parent-child dependency from the merge-request bead to it.
func (c *Client) CreateProcessingCycle(ctx context.Context, in CreateProcessingCycleInput) (string, error) {
	if in.PRBeadID == "" {
		return "", errors.New("processing-cycle: pr bead id required")
	}
	key := in.Key
	if key == "" {
		key = in.PRBeadID
	}
	fullTitle := processingCycleTitle(key)
	desc := in.Description
	if strings.TrimSpace(desc) == "" {
		desc = fullTitle
	}
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", desc,
		"--silent",
	}
	if in.Mine {
		createArgs = append(createArgs, "-l", "mine")
	}
	for _, l := range in.Labels {
		if strings.TrimSpace(l) != "" {
			createArgs = append(createArgs, "-l", l)
		}
	}
	out, err := c.Runner.Run(ctx, createArgs...)
	if err != nil {
		return "", fmt.Errorf("create processing-cycle: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the processing-cycle bead depends on (is a child
	// of) the merge-request bead.
	if _, err := c.Runner.Run(
		ctx,
		"dep", "add", id, in.PRBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		// Best-effort: log via the error chain. The bead itself was
		// created successfully so we still return the ID.
		return id, fmt.Errorf("link processing-cycle %s to pr %s: %w", id, in.PRBeadID, err)
	}
	return id, nil
}

// AppendProcessingCycleNote appends note to an existing cycle's notes and, in
// the SAME `bd update`, swaps the caller's marker label — add addLabel, drop
// every entry of removeLabels. One call is required, not two: interrupted
// between them the bead would carry a note whose marker says it was never
// recorded (or the reverse), and the next tick would append a duplicate.
//
// Callers MUST only invoke this when the note is genuinely new (see the
// diff-before-write reasoning on EnsureMergeRequest): every `bd update` is a
// Dolt commit, and the daemon re-asserts state every tick.
func (c *Client) AppendProcessingCycleNote(ctx context.Context, id, note, addLabel string, removeLabels []string) error {
	if id == "" {
		return errors.New("processing-cycle: id required")
	}
	args := []string{"update", id}
	if strings.TrimSpace(note) != "" {
		args = append(args, "--append-notes", note)
	}
	if strings.TrimSpace(addLabel) != "" {
		args = append(args, "--add-label", addLabel)
	}
	for _, l := range removeLabels {
		if strings.TrimSpace(l) != "" && l != addLabel {
			args = append(args, "--remove-label", l)
		}
	}
	if len(args) == 2 {
		return nil // nothing to write
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		return fmt.Errorf("append processing-cycle note %s: %w", id, err)
	}
	return nil
}

// ResolveProcessingCycle resolves the process-feedback bead situation for a PR,
// keyed on key (ProcessingCycleKey) with the parent-child edge to prBeadID as a
// fallback. Either may be empty, but not both.
//
// Resolution order, and why:
//
//  1. OPEN beads whose title is exactly processingCycleTitle(key). This is the
//     (repo, pr_number) key, so it finds the live cycle no matter which
//     merge-request bead parents it — the duplicate-parent hole.
//  2. OPEN beads that are children of prBeadID and carry the title prefix. This
//     is the pre-pg2-onq1e lookup, kept so a cycle created under a legacy title
//     (e.g. titled by bead id) still suppresses a duplicate.
//  3. Only when nothing is open: the NEWEST CLOSED bead for key, so the caller
//     can reference the predecessor instead of silently duplicating it.
//
// Every bd error PROPAGATES. The old code's dep query swallowed errors as
// `false`, so one transient bd/dolt failure read as "no open cycle" and the
// caller created a SECOND cycle for a PR that already had one. Returning an
// error makes the caller skip creation and retry on the next sync instead.
func (c *Client) ResolveProcessingCycle(ctx context.Context, key, prBeadID string) (ProcessingCycleState, error) {
	if key == "" && prBeadID == "" {
		return ProcessingCycleState{}, errors.New("processing-cycle: key or pr bead id required")
	}
	open, err := c.listProcessingCycles(ctx, "--status=open")
	if err != nil {
		return ProcessingCycleState{}, err
	}
	if key != "" {
		if hit := pickCycleByTitle(open, processingCycleTitle(key)); hit != nil {
			return ProcessingCycleState{Open: hit}, nil
		}
	}
	if prBeadID != "" {
		childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
		if err != nil {
			return ProcessingCycleState{}, fmt.Errorf("resolve processing-cycle: list children of %s: %w", prBeadID, err)
		}
		if hit := pickCycleByParent(open, childIDs); hit != nil {
			return ProcessingCycleState{Open: hit}, nil
		}
	}
	if key == "" {
		return ProcessingCycleState{}, nil
	}
	closed, err := c.listProcessingCycles(ctx, "--status=closed")
	if err != nil {
		return ProcessingCycleState{}, err
	}
	return ProcessingCycleState{Closed: pickNewestCycleByTitle(closed, processingCycleTitle(key))}, nil
}

// listProcessingCycles lists every `task` bead carrying the process-feedback
// title prefix in the given selector. The selector is a bd row filter, not
// necessarily a status one: `--status=open` / `--status=closed` narrow to one
// status, and `--all` takes EVERY status including closed.
//
// A selector is mandatory and MUST NOT be dropped to mean "everything": `bd
// list` excludes closed beads by DEFAULT, so an omitted selector reads as
// open-only (silently), and an empty string would also pass an empty argv
// element and corrupt the error message below.
func (c *Client) listProcessingCycles(ctx context.Context, statusFlag string) ([]ProcessingCycle, error) {
	out, err := c.Runner.Run(
		ctx,
		"list",
		"--type=task",
		statusFlag,
		"--json",
		"--limit=0",
	)
	if err != nil {
		return nil, fmt.Errorf("list processing-cycles (%s): %w", statusFlag, err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	cycles := make([]ProcessingCycle, 0, len(issues))
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, processingCycleTitlePrefix) {
			continue
		}
		cycles = append(cycles, ProcessingCycle{
			ID:           iss.ID,
			Title:        iss.Title,
			Status:       iss.Status,
			Description:  iss.Description,
			Labels:       iss.Labels,
			CreatedAt:    iss.CreatedAt,
			Dependencies: dependenciesFromBD(iss.Dependencies),
		})
	}
	return cycles, nil
}

// pickCycleByTitle returns the cycle whose title matches exactly. Exact match,
// never a prefix: `process-feedback: o/r#7` must not match `…#70`. When several
// match (the duplication this fix exists to stop) the lexicographically
// smallest ID wins, so the pick is deterministic across ticks regardless of the
// order bd returns rows in.
func pickCycleByTitle(cycles []ProcessingCycle, title string) *ProcessingCycle {
	var best *ProcessingCycle
	for i := range cycles {
		if cycles[i].Title != title {
			continue
		}
		if best == nil || cycles[i].ID < best.ID {
			best = &cycles[i]
		}
	}
	return best
}

// pickCycleByParent returns the first cycle that is a child of the PR bead
// (deterministically, lowest ID wins).
func pickCycleByParent(cycles []ProcessingCycle, childIDs []string) *ProcessingCycle {
	if len(childIDs) == 0 {
		return nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	var best *ProcessingCycle
	for i := range cycles {
		if _, ok := isChild[cycles[i].ID]; !ok {
			continue
		}
		if best == nil || cycles[i].ID < best.ID {
			best = &cycles[i]
		}
	}
	return best
}

// pickNewestCycleByTitle returns the most recently created cycle with an exact
// title match — the predecessor a successor must reference. Ties on CreatedAt
// break on the larger ID so the pick is deterministic.
func pickNewestCycleByTitle(cycles []ProcessingCycle, title string) *ProcessingCycle {
	var best *ProcessingCycle
	for i := range cycles {
		if cycles[i].Title != title {
			continue
		}
		if best == nil || cycles[i].CreatedAt > best.CreatedAt ||
			(cycles[i].CreatedAt == best.CreatedAt && cycles[i].ID > best.ID) {
			best = &cycles[i]
		}
	}
	return best
}

// FindOpenProcessingCycle returns the open processing-cycle bead linked to the
// given merge-request bead, if one exists. Returns (id, true) on hit;
// ("", false, nil) when none open.
//
// This is the PARENT-scoped view — a thin wrapper over ResolveProcessingCycle
// with no key. Prefer ResolveProcessingCycle with a ProcessingCycleKey: the
// parent-scoped lookup cannot see a cycle hanging off a DUPLICATE
// merge-request bead for the same PR, which is how cycles accumulated.
func (c *Client) FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error) {
	if prBeadID == "" {
		return "", false, errors.New("processing-cycle: pr bead id required")
	}
	st, err := c.ResolveProcessingCycle(ctx, "", prBeadID)
	if err != nil {
		return "", false, err
	}
	if st.Open == nil {
		return "", false, nil
	}
	return st.Open.ID, true, nil
}

// CloseProcessingCycle closes a processing-cycle bead with the given
// reason. Idempotent: closing an already-closed bead is a no-op.
func (c *Client) CloseProcessingCycle(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("processing-cycle: id required")
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		// bd close on a closed bead is a no-op on recent bd; if older bd
		// errors, swallow the "already closed" path.
		if strings.Contains(err.Error(), "already closed") {
			return nil
		}
		return fmt.Errorf("close processing-cycle: %w", err)
	}
	return nil
}

// ListChildrenOfPR returns the IDs of all bd issues that have a
// parent-child dependency on prBeadID. Used by cascade-on-PR-close.
//
// bd's dependency direction model: `bd dep list <id>` defaults to
// `--direction=down`, which lists the things <id> depends on. To list
// things that depend on <id> (its children) we need `--direction=up`.
func (c *Client) ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error) {
	if prBeadID == "" {
		return nil, errors.New("pr bead id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "list", prBeadID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("list children of %s: %w", prBeadID, err)
	}
	ids := extractIDs(out)
	if len(ids) == 0 {
		return nil, nil
	}
	uniq := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == prBeadID {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	return uniq, nil
}

// extractIDs scans the bd-dep-list JSON for "id":"..." pairs and returns
// the values. The shape varies across bd versions, so we use a regex-like
// scan rather than depending on a specific layout.
func extractIDs(s string) []string {
	const key = `"id":`
	var out []string
	i := 0
	for {
		k := strings.Index(s[i:], key)
		if k < 0 {
			break
		}
		k += i + len(key)
		// Skip whitespace and an opening quote.
		for k < len(s) && (s[k] == ' ' || s[k] == '"') {
			k++
		}
		// Read until the closing quote.
		end := k
		for end < len(s) && s[end] != '"' {
			end++
		}
		if end > k {
			out = append(out, s[k:end])
		}
		i = end + 1
	}
	return out
}

// DuplicateProcessingCycles reports one process-feedback key that resolves to
// more than one bead, in ANY status. Key is the (repo, pr_number) identity from
// ProcessingCycleKey.
type DuplicateProcessingCycles struct {
	Key       string            `json:"key"`
	Canonical ProcessingCycle   `json:"canonical"`
	Excess    []ProcessingCycle `json:"excess"`
}

// FindDuplicateProcessingCycles scans every process-feedback bead in EVERY
// status — open and closed alike — and reports each (repo, pr_number) key that
// resolves to more than one: the bead count vs. distinct-PR count gap the
// operator measures with `bd list`. Like FindDuplicateMergeRequests it is
// READ-ONLY and has no mutating counterpart.
//
// The selector is STATUS-AGNOSTIC on purpose, matching
// FindDuplicateMergeRequests' `includeClosed=true`. Closing both members of a
// duplicate pair does not RESOLVE it — both beads still exist and are still
// duplicates — so an open-only scan let a pair silently leave the report and
// made the reported total decay over time (measured 63 → 59 → 58 on one db with
// nothing collapsed). A decaying total is unusable as either an operator
// reconcile baseline or a "MUST NOT increase" regression signal, so the
// population is every bead the key resolves to and the total is comparable
// across time.
//
// `--all` is REQUIRED and dropping the selector would NOT be equivalent: `bd
// list` excludes closed beads by default, so an omitted selector silently
// reproduces the very open-only bug this scan exists to avoid.
//
// ADJUDICATED duplicates are excluded (pg2-peyf0): a cycle recorded as resolving
// into a canonical cycle for the SAME key, via a `supersedes` dependency edge, is
// retired from its group before the count, so a completed reconcile actually
// moves the number. A cycle that is merely CLOSED is not adjudicated and is still
// counted — that is the status-agnostic guarantee above, and it is unchanged. The
// edges arrive in the same `bd list`, so the exclusion costs no extra bd call.
//
// RESIDUAL OVER-COUNT: unlike a merge-request bead, a process-feedback key may
// legitimately resolve to several beads OVER TIME — ResolveProcessingCycle lets
// a successor open once a predecessor has closed, and the successor's
// description names it ("Supersedes closed process-feedback bead <id>"). Such a
// pair is a lifecycle, not a duplicate. A succession that is RECORDED as a
// `supersedes` edge is now excluded by the same mechanism as an adjudication —
// but a succession recorded only in the description prose is NOT, because the
// discriminator is deliberately the edge and never the text (the operator's
// ruling on pg2-0waxt, 2026-08-14). Writing that edge in the succession path is
// pg2-0waxt's remaining half. Until it lands, such a pair is still reported,
// which is why the audit stays READ-ONLY and prints ids for a person to review
// rather than a collapse plan. Measured 2026-08-14 on the zr workspace: 0 of 690
// process-feedback beads carried a supersedes reference, so no group in that
// report was a succession.
func (c *Client) FindDuplicateProcessingCycles(ctx context.Context) ([]DuplicateProcessingCycles, error) {
	cycles, err := c.listProcessingCycles(ctx, "--all")
	if err != nil {
		return nil, err
	}
	groups := map[string][]ProcessingCycle{}
	for i := range cycles {
		key := strings.TrimPrefix(cycles[i].Title, processingCycleTitlePrefix)
		groups[key] = append(groups[key], cycles[i])
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []DuplicateProcessingCycles
	for _, k := range keys {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		g = dropAdjudicated(g,
			func(pc ProcessingCycle) string { return pc.ID },
			func(pc ProcessingCycle) []Dependency { return pc.Dependencies },
			pickAuditCanonicalCycle)
		if len(g) < 2 {
			continue // every duplicate for this key has been adjudicated away
		}
		canonical := pickAuditCanonicalCycle(g)
		dup := DuplicateProcessingCycles{Key: k, Canonical: *canonical}
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

// pickAuditCanonicalCycle chooses the bead FindDuplicateProcessingCycles names
// as "keep" for one key. Precedence mirrors pickCanonicalMergeRequest:
//
//  1. OPEN over closed — the audit prints `bd close <excess-id>` next to its
//     pick, so naming a CLOSED bead as canonical while a live cycle sits in the
//     excess list would advise an operator to close the live one. That hazard
//     only exists because the scan is status-agnostic; pickCycleByTitle's
//     smallest-ID rule alone would hit it half the time in a mixed group.
//  2. Lexicographically smallest ID — a total order, so the pick never depends
//     on bd's row order. (There is no last_synced_at on a cycle to rank by.)
//
// Callers pass one title-group, so every member already shares the exact title;
// returns nil only for an empty slice.
func pickAuditCanonicalCycle(g []ProcessingCycle) *ProcessingCycle {
	var best *ProcessingCycle
	for i := range g {
		cand := &g[i]
		if best == nil || cycleMoreCanonical(cand, best) {
			best = cand
		}
	}
	return best
}

// cycleMoreCanonical reports whether a outranks b under
// pickAuditCanonicalCycle's precedence.
func cycleMoreCanonical(a, b *ProcessingCycle) bool {
	aOpen, bOpen := a.Status != "closed", b.Status != "closed"
	if aOpen != bOpen {
		return aOpen
	}
	return a.ID < b.ID
}

// Package-level convenience wrappers using the default Client.

// CreateProcessingCycle creates a processing-cycle bead using the default
// Client.
func CreateProcessingCycle(ctx context.Context, in CreateProcessingCycleInput) (string, error) {
	return NewClient().CreateProcessingCycle(ctx, in)
}

// FindOpenProcessingCycle finds an open processing-cycle bead using the
// default Client.
func FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error) {
	return NewClient().FindOpenProcessingCycle(ctx, prBeadID)
}

// CloseProcessingCycle closes a processing-cycle bead using the default
// Client.
func CloseProcessingCycle(ctx context.Context, id, reason string) error {
	return NewClient().CloseProcessingCycle(ctx, id, reason)
}
