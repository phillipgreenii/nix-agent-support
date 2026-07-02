// Package sync — draft-review consumption hook (pg2-4c5i.36).
//
// reviewHookCycle is a daemon-side Polling Consumer: once per maintenance tick
// it scans `bd ready` for draft-review beads, re-triggers stale ones whose PR
// head advanced past the last agent review, claims one, DELEGATES LLM review
// production to a spawned agent (behind the injectable Spawner seam), writes a
// reviewstage.Result sidecar, routes by ownership to a mine/team sink (Content-
// Based Router), closes the bead + records the reviewed head SHA, and applies
// retry + dead-letter with graceful failure.
//
// The daemon (Go) CANNOT run the Claude Task-tool orchestrator itself, so
// production is delegated to the Spawner (production default: `claude -p`
// running pg-pr-review-orchestrator; tests inject a fake). Everything else —
// claim, route, close, re-review gate, dead-letter — is owned by this Go code.
package sync

import (
	"context"
	"log/slog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// maxReviewFailures is the consecutive-failure cap before a draft-review bead
// is dead-lettered (blocked + needs-human) rather than re-spawned (design
// §2.3.4, R7).
const maxReviewFailures = 3

// Compile-time assertion: the production *beads.Client satisfies the review
// hook's bd surface, so the CLI/daemon can inject it directly.
var _ ReviewBeadClient = (*beads.Client)(nil)

// ReviewRef identifies one draft-review production job handed to the Spawner.
type ReviewRef struct {
	BeadID string
	Repo   string
	Number int
	Mine   bool
}

// Spawner runs the review-production agent for one PR and returns the head SHA
// the review worktree was checked out at. Injectable so tests never exec
// `claude -p`. Production default: spawn `claude -p` running the
// pg-pr-review-orchestrator, which stages a Draft via `pg-pr review draft` and
// reports the checked-out head SHA on stdout (design §2.3.1, §2.3.5).
//
// A non-nil error (or a nil error with no Draft staged) means production
// failed; the hook then leaves the bead open + unclaimed and never closes it.
type Spawner interface {
	Produce(ctx context.Context, ref ReviewRef) (headSHA string, err error)
}

// ReviewSink is the mine/team output-routing seam (Strategy). It receives the
// produced Result and applies it (mine → feedback/merge loop in .34; team →
// GitHub PENDING review in .35). Both sinks are STUBS in this slice;
// pg2-4c5i.34 and .35 fill them behind THIS EXACT signature. A sink MUST be
// idempotent and MUST treat a missing Draft/Result as "not yet produced".
type ReviewSink func(ctx context.Context, result reviewstage.Result) error

// ReviewBeadClient is the bd surface the review hook needs. Implemented by
// *beads.Client; tests inject a fake. Segregated from BeadClient
// (Interface Segregation) — the hook only needs these operations.
type ReviewBeadClient interface {
	ListReadyDraftReviews(ctx context.Context) ([]beads.DraftReviewRef, error)
	ClaimDraftReview(ctx context.Context, id string) error
	UnclaimDraftReview(ctx context.Context, id string) error
	CloseDraftReview(ctx context.Context, id, reason string) error
	ReopenDraftReview(ctx context.Context, id string) error
	DeadLetterDraftReview(ctx context.Context, id string) error
	ReviewFailCount(ctx context.Context, id string) (int, error)
	BumpReviewFailCount(ctx context.Context, id string) (int, error)
	ResetReviewFailCount(ctx context.Context, id string) error
	FindDraftReviewForPR(ctx context.Context, repo string, number int) (id string, closed bool, found bool, err error)
}

// ReviewHookDeps bundles the injectable dependencies for the review hook. When
// any of Beads/Spawner is nil the hook is disabled (a no-op) — so a daemon that
// does not configure review production keeps running unchanged.
type ReviewHookDeps struct {
	// Beads is the bd surface for draft-review claim/close/reopen/dead-letter.
	Beads ReviewBeadClient
	// Spawner delegates LLM review production. Nil disables the hook.
	Spawner Spawner
	// MineSink / TeamSink route the produced Result by ownership. Nil sinks
	// default to a logging no-op stub (this slice; .34/.35 inject real ones).
	MineSink ReviewSink
	TeamSink ReviewSink
	// ReviewsDir overrides the reviewstage directory. Empty →
	// reviewstage.DefaultDir(). Tests inject a temp dir.
	ReviewsDir string
}

// reviewHookEnabled reports whether the hook has enough deps to run.
func (e *Engine) reviewHookEnabled() bool {
	return e.deps.Review.Beads != nil && e.deps.Review.Spawner != nil
}

// reviewsDir resolves the on-disk reviews directory.
func (e *Engine) reviewsDir() string {
	if e.deps.Review.ReviewsDir != "" {
		return e.deps.Review.ReviewsDir
	}
	return reviewstage.DefaultDir()
}

// reviewHookCycle runs one pass of the draft-review consumer. Errors are logged,
// never returned: one bead's failure MUST NOT abort the tick (per-item
// continue, mirroring the ingest loop). It:
//  1. reopens closed draft-review beads whose PR head advanced past the last
//     agent review (re-review-on-head-advance, §2.3.3);
//  2. scans ready draft-review beads and produces/routes/closes each.
func (e *Engine) reviewHookCycle(ctx context.Context, log *slog.Logger) {
	if !e.reviewHookEnabled() {
		return
	}
	e.reopenStaleReviews(ctx, log)

	bdc := e.deps.Review.Beads
	refs, err := bdc.ListReadyDraftReviews(ctx)
	if err != nil {
		log.Warn("review hook: list ready draft-reviews failed", "err", err.Error())
		return
	}
	for _, ref := range refs {
		e.processDraftReview(ctx, log, ref)
	}
}

// reopenStaleReviews reopens closed draft-review beads whose PR head SHA has
// advanced past the revision last stamped reviewed_by_agent_at. The reopened
// bead re-enters `bd ready` and is produced against the new head in the same
// (or a later) tick. Store-less engines skip this pass.
func (e *Engine) reopenStaleReviews(ctx context.Context, log *slog.Logger) {
	db := e.deps.Store
	if db == nil {
		return
	}
	bdc := e.deps.Review.Beads
	for _, rcfg := range e.cfg().Repos {
		prs, err := db.ListOpenPRs(ctx, rcfg.Remote)
		if err != nil {
			log.Warn("review hook: list open PRs failed", "repo", rcfg.Remote, "err", err.Error())
			continue
		}
		for _, pr := range prs {
			if !e.headAdvancedPastAgentReview(ctx, db, pr.ID) {
				continue
			}
			id, closed, found, err := bdc.FindDraftReviewForPR(ctx, pr.Repo, pr.Number)
			if err != nil {
				log.Warn("review hook: find draft-review failed", "repo", pr.Repo, "number", pr.Number, "err", err.Error())
				continue
			}
			if !found || !closed {
				continue // no closed bead to reopen (open ones are already handled by the ready scan)
			}
			if err := bdc.ReopenDraftReview(ctx, id); err != nil {
				log.Warn("review hook: reopen draft-review failed", "id", id, "err", err.Error())
				continue
			}
			log.Info("review hook: reopened stale draft-review (head advanced)", "id", id, "repo", pr.Repo, "number", pr.Number)
		}
	}
}

// headAdvancedPastAgentReview reports whether the PR's latest revision head SHA
// differs from the revision that carries reviewed_by_agent_at — i.e. a review
// was produced at some head, but the head has since advanced. Returns false
// when no agent review has ever been produced (a fresh PR is handled by the
// normal ready scan, not by reopening).
func (e *Engine) headAdvancedPastAgentReview(ctx context.Context, db *store.DB, prID int64) bool {
	revs, err := db.ListRevisions(ctx, prID)
	if err != nil || len(revs) == 0 {
		return false
	}
	latest := revs[len(revs)-1]
	if latest.ReviewedByAgentAt != "" {
		return false // latest head already agent-reviewed
	}
	// Some earlier revision was agent-reviewed and the head has since moved.
	for _, r := range revs {
		if r.ReviewedByAgentAt != "" {
			return true
		}
	}
	return false
}

// processDraftReview handles one ready draft-review bead end to end. All errors
// are logged and swallowed here (per-item continue in the caller).
func (e *Engine) processDraftReview(ctx context.Context, log *slog.Logger, ref beads.DraftReviewRef) {
	bdc := e.deps.Review.Beads
	dir := e.reviewsDir()

	if err := bdc.ClaimDraftReview(ctx, ref.ID); err != nil {
		log.Warn("review hook: claim failed", "id", ref.ID, "err", err.Error())
		return
	}

	headSHA, prodErr := e.deps.Review.Spawner.Produce(ctx, ReviewRef{
		BeadID: ref.ID, Repo: ref.Repo, Number: ref.Number, Mine: ref.Mine,
	})

	// Production failed OR no Draft was staged → graceful failure: never close
	// a bead whose Draft was not produced.
	_, draftErr := reviewstage.Load(dir, ref.Repo, ref.Number)
	if prodErr != nil || draftErr != nil {
		e.handleProductionFailure(ctx, log, ref, prodErr, draftErr)
		return
	}

	ownership := "team"
	if ref.Mine {
		ownership = "mine"
	}
	result := reviewstage.Result{
		Repo:      ref.Repo,
		PR:        ref.Number,
		Ownership: ownership,
		HeadSHA:   headSHA,
		BeadID:    ref.ID,
	}
	if _, err := reviewstage.SaveResult(dir, &result); err != nil {
		log.Warn("review hook: save Result failed; leaving bead open", "id", ref.ID, "err", err.Error())
		if uerr := bdc.UnclaimDraftReview(ctx, ref.ID); uerr != nil {
			log.Warn("review hook: unclaim failed", "id", ref.ID, "err", uerr.Error())
		}
		return
	}

	if err := e.routeReview(ctx, ownership, result); err != nil {
		log.Warn("review hook: sink failed; leaving bead open", "id", ref.ID, "ownership", ownership, "err", err.Error())
		if uerr := bdc.UnclaimDraftReview(ctx, ref.ID); uerr != nil {
			log.Warn("review hook: unclaim failed", "id", ref.ID, "err", uerr.Error())
		}
		return
	}

	// Record the reviewed head SHA (re-review gate) before closing.
	e.stampAgentReviewed(ctx, log, ref.Repo, ref.Number, headSHA)

	if err := bdc.CloseDraftReview(ctx, ref.ID, "reviewed"); err != nil {
		log.Warn("review hook: close failed", "id", ref.ID, "err", err.Error())
		return
	}
	if err := bdc.ResetReviewFailCount(ctx, ref.ID); err != nil {
		log.Warn("review hook: reset fail count failed", "id", ref.ID, "err", err.Error())
	}
	log.Info("review hook: produced+routed+closed", "id", ref.ID, "repo", ref.Repo, "number", ref.Number, "ownership", ownership, "head_sha", headSHA)
}

// handleProductionFailure leaves the bead open + unclaimed, bumps the failure
// count, and dead-letters once the cap is reached.
func (e *Engine) handleProductionFailure(ctx context.Context, log *slog.Logger, ref beads.DraftReviewRef, prodErr, draftErr error) {
	bdc := e.deps.Review.Beads
	reason := "no Draft staged"
	if prodErr != nil {
		reason = prodErr.Error()
	}
	log.Warn("review hook: production failed; leaving bead open+unclaimed", "id", ref.ID, "repo", ref.Repo, "number", ref.Number, "reason", reason)

	if err := bdc.UnclaimDraftReview(ctx, ref.ID); err != nil {
		log.Warn("review hook: unclaim failed", "id", ref.ID, "err", err.Error())
	}
	n, err := bdc.BumpReviewFailCount(ctx, ref.ID)
	if err != nil {
		log.Warn("review hook: bump fail count failed", "id", ref.ID, "err", err.Error())
		return
	}
	if n >= maxReviewFailures {
		if err := bdc.DeadLetterDraftReview(ctx, ref.ID); err != nil {
			log.Error("review hook: dead-letter failed", "id", ref.ID, "err", err.Error())
			return
		}
		log.Error("review hook: dead-lettered draft-review after repeated failures", "id", ref.ID, "failures", n)
	}
	_ = draftErr // draftErr only distinguishes the failure reason (already logged)
}

// routeReview dispatches the produced Result to the ownership-selected sink
// (Content-Based Router). Nil sinks fall back to a logging no-op stub.
func (e *Engine) routeReview(ctx context.Context, ownership string, result reviewstage.Result) error {
	if ownership == "mine" {
		if e.deps.Review.MineSink != nil {
			return e.deps.Review.MineSink(ctx, result)
		}
		return stubMineSink(ctx, result)
	}
	if e.deps.Review.TeamSink != nil {
		return e.deps.Review.TeamSink(ctx, result)
	}
	return stubTeamSink(ctx, result)
}

// stampAgentReviewed records the reviewed head SHA on the matching revision so
// the re-review gate can detect a later head advance. Store-less engines skip.
func (e *Engine) stampAgentReviewed(ctx context.Context, log *slog.Logger, repo string, number int, headSHA string) {
	db := e.deps.Store
	if db == nil || headSHA == "" {
		return
	}
	pr, err := db.GetPR(ctx, repo, number)
	if err != nil || pr == nil {
		if err != nil {
			log.Warn("review hook: GetPR for stamp failed", "repo", repo, "number", number, "err", err.Error())
		}
		return
	}
	if err := db.MarkRevisionAgentReviewed(ctx, pr.ID, headSHA, e.deps.Now().Format("2006-01-02T15:04:05Z07:00")); err != nil {
		log.Warn("review hook: mark agent-reviewed failed", "repo", repo, "number", number, "err", err.Error())
	}
}

// stubMineSink is the .34 placeholder: it logs ownership and no-ops. pg2-4c5i.34
// replaces it behind ReviewSink. It MUST NOT call any GitHub-write path.
func stubMineSink(_ context.Context, result reviewstage.Result) error {
	slog.Default().Info("review hook: mine sink stub (no-op)", "repo", result.Repo, "pr", result.PR, "bead", result.BeadID, "head_sha", result.HeadSHA)
	return nil
}

// stubTeamSink is the .35 placeholder: it logs ownership and no-ops. pg2-4c5i.35
// replaces it behind ReviewSink. It MUST NOT post to GitHub in this slice.
func stubTeamSink(_ context.Context, result reviewstage.Result) error {
	slog.Default().Info("review hook: team sink stub (no-op)", "repo", result.Repo, "pr", result.PR, "bead", result.BeadID, "head_sha", result.HeadSHA)
	return nil
}
