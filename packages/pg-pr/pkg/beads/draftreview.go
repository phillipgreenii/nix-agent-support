// Package beads — draft-review bead wrappers (bd type=task).
//
// A draft-review bead represents the obligation to review one PR. It is a
// child of the merge-request bead, created by the beadsbridge when sync first
// detects a PR (for my PRs, or teammate PRs once out of draft). An agent claims
// it and produces the review; routing the review output is handled separately
// (pg2-4c5i.34 / .35).
//
// It reuses the builtin bd `task` type and is discriminated by a title prefix,
// exactly like the processing-cycle bead — so no custom-type config is needed.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// draftReviewTitlePrefix is matched verbatim by findDraftReviewChild.
const draftReviewTitlePrefix = "draft-review: "

// DraftReviewRef is a ready draft-review bead resolved to its target PR.
// Repo/Number are parsed from the title (`draft-review: <owner/repo>#<n>`);
// Mine is derived from the `mine` ownership label (absent ⇒ teammate PR).
type DraftReviewRef struct {
	ID     string
	Repo   string
	Number int
	Mine   bool
}

// ListReadyDraftReviews returns the ready (unblocked, unclaimed-or-claimed but
// still open) draft-review beads, resolved to their target PRs. It shells out
// to `bd ready --json`, filters to `task` beads whose title carries the
// draft-review prefix, and parses `<owner/repo>#<number>` from the title plus
// the `mine` label. Beads whose title does not parse are skipped (not fatal).
//
// This is the daemon-side detection primitive for pg2-4c5i.36 and the backing
// query for `pg-pr review ready --json`.
func (c *Client) ListReadyDraftReviews(ctx context.Context) ([]DraftReviewRef, error) {
	out, err := c.Runner.Run(ctx, "ready", "--json")
	if err != nil {
		return nil, fmt.Errorf("list ready draft-reviews: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	var refs []DraftReviewRef
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		repo, number, ok := parseRepoPR(strings.TrimPrefix(iss.Title, draftReviewTitlePrefix))
		if !ok {
			continue // malformed title — skip, do not abort the whole scan
		}
		refs = append(refs, DraftReviewRef{
			ID:     iss.ID,
			Repo:   repo,
			Number: number,
			Mine:   hasLabel(iss.Labels, "mine"),
		})
	}
	return refs, nil
}

// parseRepoPR splits "<owner>/<repo>#<number>" into (repo, number). Returns
// ok=false when the shape does not match (missing '#', non-numeric number, or
// empty repo).
func parseRepoPR(s string) (repo string, number int, ok bool) {
	s = strings.TrimSpace(s)
	hash := strings.LastIndex(s, "#")
	if hash <= 0 || hash == len(s)-1 {
		return "", 0, false
	}
	repo = s[:hash]
	n, err := strconv.Atoi(s[hash+1:])
	if err != nil || n <= 0 || !strings.Contains(repo, "/") {
		return "", 0, false
	}
	return repo, n, true
}

// EnsureDraftReviewBead ensures exactly one draft-review bead (open OR closed)
// exists as a child of prBeadID, and returns its ID. title is appended after
// the canonical prefix; mine adds the `mine` ownership label so downstream
// routing (pg2-4c5i.34/.35) can distinguish my PRs from teammate PRs.
//
// Idempotent on re-delivery: if a draft-review child already exists it is
// returned without creating a second. It MUST NOT resurrect a closed
// draft-review bead — a completed review (closed bead) suppresses recreation.
// A lookup error PROPAGATES (the caller skips and retries next tick); it is
// never treated as "none exists" (that is the duplicate-cycle bug,
// processingcycle.go:84-90).
func (c *Client) EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	if prBeadID == "" {
		return "", errors.New("draft-review: pr bead id required")
	}
	existing, err := c.findDraftReviewChild(ctx, prBeadID)
	if err != nil {
		return "", err // propagate — do NOT treat as "none exists"
	}
	if existing != "" {
		return existing, nil // open or closed → do not recreate
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := draftReviewTitlePrefix + title
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	}
	if mine {
		createArgs = append(createArgs, "-l", "mine")
	}
	out, err := c.Runner.Run(ctx, createArgs...)
	if err != nil {
		return "", fmt.Errorf("create draft-review: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the draft-review bead depends on (is a child of) the
	// merge-request bead.
	if _, err := c.Runner.Run(
		ctx,
		"dep", "add", id, prBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link draft-review %s to pr %s: %w", id, prBeadID, err)
	}
	return id, nil
}

// EnsureDraftReviewMineLabel adds the "mine" ownership label to the OPEN
// draft-review child of prBeadID when it lacks it — used on a team->co-owned
// transition so routing treats the review as mine-style. Closed (completed)
// review beads are left untouched. Idempotent; a lookup error PROPAGATES.
func (c *Client) EnsureDraftReviewMineLabel(ctx context.Context, prBeadID string) error {
	if prBeadID == "" {
		return errors.New("draft-review: pr bead id required")
	}
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return fmt.Errorf("relabel draft-review: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(ctx, "list", "--type=task", "--json", "--limit=0") // open only (no --all)
	if err != nil {
		return fmt.Errorf("relabel draft-review: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; !ok {
			continue
		}
		if hasLabel(iss.Labels, "mine") {
			return nil // already mine-style
		}
		_, err := c.Runner.Run(ctx, "update", iss.ID, "--add-label", "mine")
		return err
	}
	return nil
}

// FindDraftReviewForPR resolves the draft-review bead (open OR closed) for a
// given upstream PR, mapping (repo, number) → merge-request bead → draft-review
// child. It returns the child id, whether it is currently closed, whether one
// was found at all, and any error. Used by the re-review-on-head-advance gate
// (pg2-4c5i.36 §2.3.3) to reopen a closed draft-review bead when the PR head
// advances past the last agent-reviewed SHA.
func (c *Client) FindDraftReviewForPR(ctx context.Context, repo string, number int) (id string, closed bool, found bool, err error) {
	mrs, err := c.ListMergeRequests(ctx, true)
	if err != nil {
		return "", false, false, fmt.Errorf("find draft-review for %s#%d: list MRs: %w", repo, number, err)
	}
	var mrID string
	for _, mr := range mrs {
		if mr.Fields.Repo == repo && mr.Fields.PRNumber == number {
			mrID = mr.ID
			break
		}
	}
	if mrID == "" {
		return "", false, false, nil
	}
	childIDs, err := c.ListChildrenOfPR(ctx, mrID)
	if err != nil {
		return "", false, false, fmt.Errorf("find draft-review for %s#%d: list children: %w", repo, number, err)
	}
	if len(childIDs) == 0 {
		return "", false, false, nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, cid := range childIDs {
		isChild[cid] = struct{}{}
	}
	out, err := c.Runner.Run(ctx, "list", "--type=task", "--all", "--json", "--limit=0")
	if err != nil {
		return "", false, false, fmt.Errorf("find draft-review for %s#%d: list tasks: %w", repo, number, err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", false, false, err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, iss.Status == "closed", true, nil
		}
	}
	return "", false, false, nil
}

// findDraftReviewChild returns the ID of an existing draft-review child of
// prBeadID (open OR closed), or "" when none. Strategy mirrors
// FindOpenProcessingCycle but INCLUDES closed beads (so a completed review
// suppresses recreation): resolve the PR's children once (ListChildrenOfPR),
// then intersect with `task` beads (open + closed, via --all) whose title
// carries the draft-review prefix. Every bd error PROPAGATES.
func (c *Client) findDraftReviewChild(ctx context.Context, prBeadID string) (string, error) {
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return "", nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(
		ctx,
		"list",
		"--type=task",
		"--all", // include closed — required for no-resurrection
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, nil
		}
	}
	return "", nil
}
