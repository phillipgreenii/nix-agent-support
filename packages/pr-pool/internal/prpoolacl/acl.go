// Package prpoolacl is the pr-pool anti-corruption layer over pg-pr. It reads
// pg-pr's PR data (`pg-pr pr list --json`) and idempotently projects a review-pr
// work bead (child of the pg-pr-owned merge-request bead) per open PR, gated on
// pg-pr:active-pr. It runs as a PRE-DRAIN step (not a role-query: query<->role
// are coupled), writing beads the downstream review role discovers via bd ready.
package prpoolacl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// activePRGate is the custom gate type that holds a review-pr bead out of bd
// ready until the ACL confirms (from pg-pr facts) the PR is still open. Custom
// pg-pr:* gate types have no bd auto-resolver, so the ACL resolves them itself.
const activePRGate = "pg-pr:active-pr"

// PR is the subset of `pg-pr pr list --json` the ACL consumes (base fields).
type PR struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"branch"`
	State     string `json:"state"`
	Ownership string `json:"ownership"`
}

// The ownership values pg-pr emits on the seam. The set is CLOSED at three
// values, mirroring pg-pr's ownership package
// (packages/pg-pr/internal/ownership/ownership.go:11-13).
const (
	ownershipMine    = "mine"
	ownershipCoOwned = "co-owned"
	ownershipTeam    = "team"
)

// actsAsMine reports whether a PR's ownership makes the ACL treat it like my own
// for SELECTION (reviewed even while a GitHub draft). It is the pr-pool-side copy
// of pg-pr's ownership.Ownership.ActsAsMine — `o == Mine || o == CoOwned`
// (packages/pg-pr/internal/ownership/ownership.go:46) — which over the closed
// 3-value set is exactly pg-pr's beadsbridge formulation `p.Ownership != "team"`
// (packages/pg-pr/internal/beadsbridge/bridge.go:111).
//
// The predicate is DUPLICATED, not shared, and cannot be shared today: pr-pool is
// a separate Go module (github.com/phillipgreenii/pr-pool) and pg-pr's ownership
// package sits under pg-pr's internal/, which Go refuses to import across the
// module boundary ("use of internal package ... not allowed"). Nor should it be
// shared: prpoolacl is an anti-corruption layer over the `pg-pr pr list --json`
// CLI seam and deliberately owns its own copy of pg-pr's vocabulary (see PR
// above) instead of compiling against pg-pr's types. TestActsAsMineParity pins
// the two formulations together over the closed set so they cannot drift again.
//
// An out-of-band value (including "", the field absent from the seam) is
// deliberately NOT acts-as-mine: it degrades to team-style selection, the
// conservative direction (such a draft is skipped, never auto-reviewed).
func actsAsMine(ownership string) bool {
	return ownership == ownershipMine || ownership == ownershipCoOwned
}

func prKey(pr PR) string { return fmt.Sprintf("%s#%d", pr.Repo, pr.Number) }

// Reconcile ensures a review-pr work bead for each open PR and resolves its
// active-pr gate from the fact that the PR is still open. It is idempotent
// (re-run = no dupes) and exit-0-on-partial: per-PR failures are collected and
// returned, never aborting the pass — a non-zero abort would strand the
// following drain's other roles (H6). Returns the ensured review-pr bead ids.
func Reconcile(ctx context.Context, r beads.Runner, prs []PR) (reviewIDs []string, errs []error) {
	// Snapshot the merge-request and review-pr (task) beads ONCE — including
	// closed (--all) so a completed review-pr is not resurrected, and so we
	// don't spawn a `bd list` per PR. A snapshot failure is fatal for the pass
	// (without it we cannot find-or-reuse safely, and creating blind would risk
	// dupes), returned as a single error so the caller exit-0's the pass.
	mrs, err := beads.List(ctx, r, "--type="+beads.MergeRequestType, "--all")
	if err != nil {
		return nil, []error{fmt.Errorf("acl: list merge-request beads: %w", err)}
	}
	reviews, err := beads.List(ctx, r, "--type=task", "--all")
	if err != nil {
		return nil, []error{fmt.Errorf("acl: list task beads: %w", err)}
	}

	// Phase 1 — ensure the review-pr child bead + its active-pr gate exist.
	for _, pr := range prs {
		id, err := ensureReview(ctx, r, pr, mrs, reviews)
		if err != nil {
			errs = append(errs, err)
		}
		if id != "" {
			reviewIDs = append(reviewIDs, id)
		}
	}
	// Phase 2 — watcher: resolve the active-pr gate for every PR still open. A
	// SEPARATE pass (not create+resolve inline) so a crash between gate-create
	// and resolve self-heals on the next run, and so a resolved gate (absent from
	// the open-only gate list) is never re-created into a re-block.
	gates, err := beads.ListGates(ctx, r)
	if err != nil {
		errs = append(errs, fmt.Errorf("acl: list gates: %w", err))
		return reviewIDs, errs
	}
	for _, pr := range prs {
		g := beads.FindOpenGate(gates, activePRGate, prKey(pr))
		if g == nil {
			continue
		}
		if err := beads.ResolveGate(ctx, r, g.ID, "pg-pr reports PR open/active"); err != nil {
			errs = append(errs, err)
		}
	}
	return reviewIDs, errs
}

// ensureReview finds-or-reuses the pg-pr merge-request bead for pr (NH2: it
// NEVER creates one — pg-pr's sync daemon is the sole MR producer) from the mrs
// snapshot, then ensures a review-pr child bead carrying the PR coords. Returns
// the review-pr id (an existing OPEN bead; a REOPENED closed bead whose PR head
// advanced past the reviewed sha; or a newly created one), or "" when the PR is
// skipped: a team PR still in draft (co-owned counts as mine), no MR bead yet, a
// closed MR, or a completed (closed) review whose head has NOT advanced (not
// resurrected). On the birth path it also creates the active-pr gate exactly once
// (Phase 2 resolves).
func ensureReview(ctx context.Context, r beads.Runner, pr PR, mrs, reviews []beads.Issue) (string, error) {
	// Selection parity with pg-pr's beadsbridge (draftreview): a PR that acts as
	// mine (mine OR co-owned) is reviewed even while a GitHub draft; a team PR is
	// reviewed only once it leaves draft. pg-pr's predicate lives in Handler.Handle's
	// pr.opened/pr.updated arm (packages/pg-pr/internal/beadsbridge/bridge.go:111-112):
	//
	//	mine := p.Ownership != "team" // mine OR co-owned
	//	if !h.suppressDraftReviews && (mine || !p.Draft) {
	//
	// pr.State == "draft" is the SAME fact as pg-pr's p.Draft — the seam derives
	// Draft as `pr.State == "draft"` (packages/pg-pr/cmd/pg-pr/pr_list.go:99). See
	// actsAsMine for why the predicate is copied rather than imported. (pg-pr's
	// suppressDraftReviews review kill switch has no pr-pool counterpart; it is not
	// part of this ownership/draft predicate.)
	if !actsAsMine(pr.Ownership) && pr.State == "draft" {
		return "", nil
	}

	mr := beads.MatchMergeRequest(mrs, pr.Repo, pr.Number)
	if mr == nil {
		slog.Warn("acl: no merge-request bead yet; skipping (pg-pr sync pending)", "pr", prKey(pr))
		return "", nil
	}
	if mr.Status == "closed" {
		return "", nil // do not attach a review child under a closed PR bead
	}

	if existing := beads.MatchReviewPR(reviews, pr.Repo, pr.Number); existing != nil {
		if existing.Status != "closed" {
			return existing.ID, nil // idempotent reuse of the open bead
		}
		// A closed review-pr is a COMPLETED review. Re-emit it only when the PR's
		// head advanced past the reviewed commit — the re-review-on-head-advance
		// cursor that replaces pg-pr's retired reopenStaleReviews. The reviewed sha
		// is metadata.head_sha, the exact commit the worker checked out and
		// submitted at. A missing reviewed sha (a legacy pre-cursor bead) or a
		// missing/equal current head is NOT an advance -> do NOT resurrect (C1).
		reviewedSHA, _ := existing.Metadata["head_sha"].(string)
		if reviewedSHA == "" || pr.HeadSHA == "" || pr.HeadSHA == reviewedSHA {
			return "", nil
		}
		// Reopen the SAME bead with the new head so the worker reviews the new
		// commit (it checks out metadata.head_sha; reopening without refreshing
		// would re-review the old sha forever). The PR is confirmed open this pass
		// (it is in the open list), so the reopened bead is ready immediately — no
		// active-pr gate is re-created (it would only be created and then resolved
		// in this same pass; Phase 2 resolves the birth gate for open PRs).
		if err := beads.ReopenReview(ctx, r, existing.ID, pr.HeadSHA, pr.Branch); err != nil {
			return "", fmt.Errorf("acl: reopen review-pr %s on head advance: %w", prKey(pr), err)
		}
		return existing.ID, nil
	}

	// Birth path.
	title := beads.ReviewPRTitlePrefix + prKey(pr)
	meta := map[string]any{
		"repo":      pr.Repo,
		"pr_number": pr.Number,
		"branch":    pr.Branch,
		"head_sha":  pr.HeadSHA,
	}
	id, err := beads.Create(ctx, r, "task", title, meta)
	if err != nil {
		return "", fmt.Errorf("acl: create review-pr %s: %w", prKey(pr), err)
	}
	if err := beads.LinkChild(ctx, r, id, mr.ID); err != nil {
		// The bead exists but is not linked under the MR (so cascadeClose won't
		// close it). Keep + emit it (the work is real), and report the failure —
		// it is NOT auto-repaired (a later pass reuses the existing bead and skips
		// the birth path), so an operator must fix the edge or close the orphan.
		return id, fmt.Errorf("acl: link review-pr %s under %s (unlinked orphan): %w", id, mr.ID, err)
	}
	if _, err := beads.CreateGate(ctx, r, id, activePRGate, prKey(pr), "review-pr blocked until pg-pr confirms PR active"); err != nil {
		// Best-effort: without the gate the review-pr is simply ready. Keep the
		// bead and report; Phase 2 resolves the gate only if it was created.
		return id, fmt.Errorf("acl: create active-pr gate for %s: %w", id, err)
	}
	return id, nil
}

// ReadPRList shells `pg-pr pr list --json` with cwd=repoRoot so pg-pr
// auto-detects the repo from the monorepo's git remote. Base fields only (no
// --reviewers): the cheap, network-free read seam.
func ReadPRList(ctx context.Context, repoRoot string) ([]PR, error) {
	cmd := exec.CommandContext(ctx, "pg-pr", "pr", "list", "--json")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("pg-pr pr list: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("pg-pr pr list: %w", err)
	}
	return parsePRList(out)
}

func parsePRList(b []byte) ([]PR, error) {
	var prs []PR
	if err := json.Unmarshal(b, &prs); err != nil {
		return nil, fmt.Errorf("parse pg-pr pr list: %w", err)
	}
	return prs, nil
}
