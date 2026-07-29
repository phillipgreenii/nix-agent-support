// Package prpoolacl is the pr-pool anti-corruption layer over pg-pr. It reads
// pg-pr's PR data (`pg-pr pr list --json`) and idempotently projects a review-pr
// work bead (child of the pg-pr-owned merge-request bead) per open PR, gated on
// pg-pr:active-pr. It runs as a PRE-DRAIN step (not a role-query: query<->role
// are coupled), writing beads the downstream review role discovers via bd ready.
//
// The seam is network-free, so its rows are only as current as pg-pr's last sync.
// The ACL therefore reads each row's freshness (last_synced_at + stale) and
// REFUSES to act on a row past its bound — see staleForAction / actionablePRs.
package prpoolacl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// activePRGate is the custom gate type that holds a review-pr bead out of bd
// ready until the ACL confirms (from pg-pr facts) the PR is still open. Custom
// pg-pr:* gate types have no bd auto-resolver, so the ACL resolves them itself.
const activePRGate = "pg-pr:active-pr"

// PR is the subset of `pg-pr pr list --json` the ACL consumes (base fields plus
// the row's freshness).
type PR struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"branch"`
	State     string `json:"state"`
	Ownership string `json:"ownership"`
	// LastSyncedAt is pg-pr's AS-OF time for this row (RFC3339 UTC, the store's
	// pull_request.last_synced_at column emitted verbatim). The base seam is
	// network-free, so every field above is only as true as this timestamp.
	LastSyncedAt string `json:"last_synced_at"`
	// Stale is pg-pr's OWN verdict that LastSyncedAt has aged past its freshness
	// bound. The bound is deliberately NOT re-derived here: pg-pr owns the sync
	// cadence the bound is measured against (internal/freshness), and unlike
	// actsAsMine there is no way to pin a duplicated constant across the module
	// boundary with a parity test — so the ACL consumes the VERDICT and keeps
	// only the "is there a usable as-of time at all" check for itself.
	Stale bool `json:"stale"`
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
// (packages/pg-pr/internal/ownership/ownership.go:46), which every pg-pr consumer
// now calls, including the beadsbridge draft-review selection
// (packages/pg-pr/internal/beadsbridge/bridge.go). That site used to hand-roll
// `p.Ownership != "team"`; pg2-q2drf replaced it with the shared predicate, so
// there is a single formulation on the pg-pr side and this copy mirrors it
// exactly — on the closed 3-value set AND on out-of-band values.
//
// The predicate is DUPLICATED, not shared, and cannot be shared today: pr-pool is
// a separate Go module (github.com/phillipgreenii/pr-pool) and pg-pr's ownership
// package sits under pg-pr's internal/, which Go refuses to import across the
// module boundary ("use of internal package ... not allowed"). Nor should it be
// shared: prpoolacl is an anti-corruption layer over the `pg-pr pr list --json`
// CLI seam and deliberately owns its own copy of pg-pr's vocabulary (see PR
// above) instead of compiling against pg-pr's types. TestActsAsMineParity pins
// this copy to pg-pr's predicate over the closed set AND on out-of-band values,
// so the duplication cannot drift.
//
// An out-of-band value (including "", the field absent from the seam) is
// deliberately NOT acts-as-mine: it degrades to team-style selection, the
// conservative direction (such a draft is skipped, never auto-reviewed).
func actsAsMine(ownership string) bool {
	return ownership == ownershipMine || ownership == ownershipCoOwned
}

func prKey(pr PR) string { return fmt.Sprintf("%s#%d", pr.Repo, pr.Number) }

// staleForAction reports whether pr's freshness forbids ACTING on it this pass.
// It is the ACL's answer to "don't act on stale truth" (the deployment's
// INV-FRESH-1): a readiness signal derived from data past its bound MUST NOT be
// presented as current, and resolving the pg-pr:active-pr gate is exactly such a
// signal — it asserts "pg-pr reports PR open/active", which a stale row cannot
// support.
//
// Two ways a PR is unusable:
//
//   - pg-pr FLAGGED it stale (its sync daemon is behind or stopped, so state /
//     head_sha / ownership may no longer be true); or
//   - the seam carries NO usable as-of time — the field is absent (an older
//     pg-pr that predates the freshness fields, decoding to ""), empty, or
//     unparseable. An unknown as-of is treated as stale, never as fresh: this is
//     the same fail-closed direction the ACL already takes for an out-of-band
//     ownership value (degrades to team-style selection) and for a closed
//     review-pr with no reviewed sha (not resurrected). The failure mode is a
//     loud no-op pass, which the ACL already tolerates — a `pg-pr pr list` that
//     cannot be read at all is likewise treated as zero PRs (reconcileACL, H6).
func staleForAction(pr PR) bool {
	if pr.Stale {
		return true
	}
	_, err := time.Parse(time.RFC3339, pr.LastSyncedAt)
	return err != nil
}

// actionablePRs partitions prs into the ones fresh enough to act on, RECORDING
// (WARN) every refusal with the as-of time that caused it — refuse-and-record,
// matching the ACL's existing skip channel (see the no-merge-request-bead skip).
// Filtering ONCE up front covers both of Reconcile's phases: a stale PR gets
// neither a review-pr bead nor an active-pr gate resolution.
//
// The filter is PER PR, not whole-pass: a repo mid-refresh can hold rows of
// different ages, and there is no reason to withhold action on a freshly synced
// PR because a different one is behind.
func actionablePRs(prs []PR) []PR {
	out := make([]PR, 0, len(prs))
	for _, pr := range prs {
		if staleForAction(pr) {
			slog.Warn("acl: pg-pr facts past their freshness bound; refusing to act on this PR",
				"pr", prKey(pr), "last_synced_at", pr.LastSyncedAt, "pg_pr_stale_flag", pr.Stale)
			continue
		}
		out = append(out, pr)
	}
	return out
}

// Reconcile ensures a review-pr work bead for each open PR whose pg-pr facts are
// FRESH, and resolves its active-pr gate from the fact that the PR is still open.
// It is idempotent (re-run = no dupes) and exit-0-on-partial: per-PR failures are
// collected and returned, never aborting the pass — a non-zero abort would strand
// the following drain's other roles (H6). Returns the ensured review-pr bead ids.
//
// PRs whose facts are past their freshness bound are dropped up front (see
// actionablePRs): they are neither projected into beads nor used to resolve a
// gate, and each refusal is logged. A stale row self-heals — the next pass, after
// pg-pr's sync catches up, acts on it normally.
func Reconcile(ctx context.Context, r beads.Runner, prs []PR) (reviewIDs []string, errs []error) {
	// Freshness gate FIRST, so neither phase below can act on stale truth.
	prs = actionablePRs(prs)

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
//
// Because it is network-free, the rows it returns are exactly as current as
// pg-pr's last sync — which is why each row carries LastSyncedAt + Stale, and why
// Reconcile refuses to act on the rows that are past their bound.
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
