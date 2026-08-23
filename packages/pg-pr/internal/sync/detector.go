package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fingerprintHash is a stable hash of the change-relevant fields.
func fingerprintHash(f vcs.PRFingerprint) string {
	s := fmt.Sprintf("%s|%s|%s|%s|%t|%d|%d|%d",
		f.UpdatedAt, f.HeadOID, f.StatusRollup, f.State, f.IsDraft,
		f.ReviewCount, f.CommentCount, f.ReviewThreadCount)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// diffResult is the detector's per-tick decision for one group.
type diffResult struct {
	enqueued map[prKey]bool
	reasons  map[prKey]string // added|changed|disappeared
	roster   map[prKey]string // new prev (hashes)
}

// diffRoster computes enqueues from the fresh roster vs the previous-tick
// hashes and the open-bead set. complete=false (query failed/truncated) skips
// the disappeared check (mass-close guard) but still records the roster.
// Callers pass the roster entries for one group; "added" = no open bead,
// "changed" = bead exists and fingerprint is new/differs.
func diffRoster(prev map[prKey]string, roster []vcs.PRFingerprint, openBeads map[prKey]bool, complete bool) diffResult {
	d := diffResult{
		enqueued: map[prKey]bool{},
		reasons:  map[prKey]string{},
		roster:   map[prKey]string{},
	}
	inRoster := map[prKey]bool{}
	for _, f := range roster {
		k := prKey{Repo: f.Repo, Number: f.Number}
		h := fingerprintHash(f)
		d.roster[k] = h
		inRoster[k] = true
		if old, seen := prev[k]; seen && old == h {
			continue // unchanged
		}
		d.enqueued[k] = true
		if openBeads[k] {
			d.reasons[k] = "changed"
		} else {
			d.reasons[k] = "added"
		}
	}
	if complete {
		for k := range openBeads {
			if !inRoster[k] {
				d.enqueued[k] = true
				d.reasons[k] = "disappeared"
			}
		}
	}
	return d
}

// buildMineQuery is the cross-repo "my open PRs" search (drafts included).
func buildMineQuery(cfg *config.Config) string {
	parts := []string{"is:pr", "is:open", "author:" + cfg.SelfLogin}
	for _, r := range cfg.Repos {
		parts = append(parts, "repo:"+r.Remote)
	}
	return strings.Join(parts, " ")
}

// buildTeamQuery is one repo's "team open PRs" search (drafts included; empty
// when the repo has no team members).
func buildTeamQuery(rcfg config.RepoConfig) string {
	if len(rcfg.TeamMembers) == 0 {
		return ""
	}
	parts := []string{"is:pr", "is:open", "repo:" + rcfg.Remote}
	for _, m := range rcfg.TeamMembers {
		parts = append(parts, "author:"+m)
	}
	return strings.Join(parts, " ")
}

// buildTeamQueries returns the searches whose UNION is the not-mine "to-review"
// roster for one repo: the team-authors bucket (buildTeamQuery) plus — because
// GitHub ANDs distinct qualifier types and so cannot OR labels/review-requested
// into one query — a review-requested:<self> bucket, a reviewed-by:<self>
// bucket, and one bucket per configured watch label. The broadened buckets
// exclude my own PRs (-author:<self>) so a PR I own is never surfaced as
// someone-else's-to-review (it stays in the mine roster and is still
// self-reviewed). With no self login the broadened buckets are omitted (cannot
// exclude-mine); only the authors bucket, if any, remains.
//
// The reviewed-by bucket keeps a PR I have ALREADY reviewed in the roster after
// the review request that first surfaced it is satisfied: GitHub drops a PR from
// review-requested:<self> once I submit a review, so a conversation I am part of
// would otherwise vanish from the to-review set while still open and still
// waiting on my re-review. Its snapshot counterpart is
// snapshot.MatchReasonReviewedByMe — without that re-check the PRs this bucket
// retrieves carry no match reason and Build drops them, so the two halves MUST
// ship together.
//
// Deliberately NOT retrieved: PRs I have only COMMENTED on without submitting a
// review (`commenter:` / `involves:`). A comment is not a review commitment, and
// those qualifiers pull in every mention and every issue-comment thread, which
// is a much larger and noisier set than "PRs I am reviewing".
func buildTeamQueries(rcfg config.RepoConfig, self string) []string {
	var qs []string
	if t := buildTeamQuery(rcfg); t != "" {
		qs = append(qs, t)
	}
	if self == "" {
		return qs
	}
	base := "is:pr is:open repo:" + rcfg.Remote
	qs = append(qs, base+" review-requested:"+self+" -author:"+self)
	qs = append(qs, base+" reviewed-by:"+self+" -author:"+self)
	for _, l := range rcfg.WatchLabels {
		qs = append(qs, base+` label:"`+l+`" -author:`+self)
	}
	return qs
}

// mergeRosters unions the per-bucket fingerprint rosters, de-duping by
// repo#number (a PR matching several buckets appears once; first-seen wins). The
// merge is complete only when NO bucket was truncated — an incomplete merge
// disables diffRoster's disappeared/mass-close detection, since a truncated
// bucket is missing PRs that must not be mistaken for closures.
func mergeRosters(results []vcs.FingerprintResult) (roster []vcs.PRFingerprint, complete bool) {
	complete = true
	seen := map[prKey]bool{}
	for _, res := range results {
		if res.Truncated {
			complete = false
		}
		for _, pr := range res.PRs {
			k := prKey{Repo: pr.Repo, Number: pr.Number}
			if seen[k] {
				continue
			}
			seen[k] = true
			roster = append(roster, pr)
		}
	}
	return roster, complete
}

// firstFingerprintProvider returns the first registered VCS provider that
// supports fingerprint polling.
func (e *Engine) firstFingerprintProvider() (vcs.FingerprintProvider, bool) {
	for _, p := range e.deps.VCS {
		if fp, ok := p.(vcs.FingerprintProvider); ok {
			return fp, true
		}
	}
	return nil, false
}

// firstAuthChecker returns the first registered VCS provider that supports the
// optional auth-preflight capability. Mirrors firstFingerprintProvider so the
// daemon can run a startup CheckAuth without coupling to a concrete provider.
func (e *Engine) firstAuthChecker() (vcs.AuthChecker, bool) {
	for _, p := range e.deps.VCS {
		if ac, ok := p.(vcs.AuthChecker); ok {
			return ac, true
		}
	}
	return nil, false
}

// openBeadsForGroup enumerates open merge-request beads across the given repos
// (each repo's own bd workspace), keyed by prKey, filtered to mine (mine=true)
// or team (mine=false) by author. Per-repo list errors are skipped (conservative:
// a missing repo just won't contribute "disappeared" candidates this tick).
//
// This partition is deliberately by AUTHOR (isSelfAuthored), NOT by the 3-way
// ownership.Classify — a co-owned PR (teammate-authored, my commits) intentionally
// groups as team here. The fingerprint rosters it is diffed against are built from
// author/reviewer/label GitHub queries (buildMineQuery vs buildTeamQueries), which
// have no notion of my commits, so a co-owned PR is enumerated by the TEAM query;
// grouping its bead as team keeps bead-vs-roster close-detection consistent.
// Classifying it as mine here would compare it against the author:self roster it
// never appears in, spuriously re-enqueueing it every tick. (pg2-aag72)
func (e *Engine) openBeadsForGroup(ctx context.Context, repos []config.RepoConfig, mine bool) map[prKey]bool {
	out := map[prKey]bool{}
	for _, rcfg := range repos {
		bdc := e.bdClientFor(rcfg)
		mrs, err := bdc.ListMergeRequests(ctx, false)
		if err != nil {
			continue
		}
		for _, mr := range mrs {
			if mr.Fields.Repo != rcfg.Remote {
				continue
			}
			if e.isSelfAuthored(mr.Fields.Author) == mine {
				out[prKey{Repo: mr.Fields.Repo, Number: mr.Fields.PRNumber}] = true
			}
		}
	}
	return out
}

// recordPoll emits the per-poll telemetry for a fingerprint query.
func (e *Engine) recordPoll(group string, res vcs.FingerprintResult, err error, dur time.Duration) {
	telemetry.FingerprintPollDuration.WithLabelValues(group).Observe(dur.Seconds())
	if err != nil {
		telemetry.FingerprintPollErrorsTotal.WithLabelValues(group).Inc()
		return
	}
	if res.Truncated {
		telemetry.FingerprintPollTruncatedTotal.WithLabelValues(group).Inc()
	}
	telemetry.FingerprintPollSuccessTimestamp.WithLabelValues(group).Set(float64(e.deps.Now().Unix()))
	telemetry.GraphQLCost.WithLabelValues(group).Set(float64(res.RateCost))
	telemetry.GraphQLRateRemaining.Set(float64(res.RateLeft))
	// Retain the latest rate reading for the detector's proactive <buffer skip.
	// resetAt is RFC3339 from GitHub; a parse failure leaves rateResetAt zero,
	// which disables the pause (fail-open — never wedge on a malformed reset).
	e.lastRateLeft = res.RateLeft
	if res.ResetAt != "" {
		if t, perr := time.Parse(time.RFC3339, res.ResetAt); perr == nil {
			e.rateResetAt = t
		}
	}
}

// graphQLRateBuffer is the bottom slice of GitHub's GraphQL rate-limit window
// (5000 pts/hr) pg-pr reserves for direct `gh` use. When remaining drops below
// it, the daemon proactively skips its fingerprint poll until the window resets
// rather than draining the last points out from under an interactive `gh` call.
const graphQLRateBuffer = 1000

// graphQLRatePaused reports whether the detector should skip this tick's
// fingerprint poll: the last observed remaining is below the reserve AND the
// rate window has not yet reset. Resumes automatically once now >= rateResetAt
// (the next poll then refreshes remaining to ~5000).
func (e *Engine) graphQLRatePaused(now time.Time) bool {
	return e.lastRateLeft > 0 && e.lastRateLeft < graphQLRateBuffer && now.Before(e.rateResetAt)
}

// fingerprintTick runs the mine + team fingerprint queries, diffs them against
// the previous rosters and open beads, and enqueues changed PRs. Pure detector:
// it never mutates beads/snapshot. Team drafts are NOT special-cased here — the
// query includes drafts and refreshPR decides dormant-vs-active from GetPR state.
func (e *Engine) fingerprintTick(ctx context.Context, mineQ, teamQ *refreshQueue, log *slog.Logger) {
	cfg := e.cfg()
	prov, ok := e.firstFingerprintProvider()
	if !ok {
		return // no fingerprint-capable provider (e.g. test stub) — nothing to do
	}

	// Proactive rate-limit reserve: if the last poll left fewer than
	// graphQLRateBuffer points and the window has not reset, SKIP this tick's
	// GraphQL poll entirely (same "log and skip the poll" shape as a failed
	// poll) so the bottom ~1000 points stay available for direct `gh` use.
	// This is a SELF-imposed buffer — distinct from actual 0-remaining
	// exhaustion — surfaced on its own pause gauge. Resumes automatically once
	// now >= rateResetAt (the next poll refreshes remaining to ~5000).
	if e.graphQLRatePaused(e.deps.Now()) {
		telemetry.GraphQLRatePaused.Set(1)
		log.Info("skipping GraphQL fingerprint poll: self-imposed <1000 safety buffer",
			"remaining", e.lastRateLeft, "buffer", graphQLRateBuffer, "reset_at", e.rateResetAt.Format(time.RFC3339))
		return
	}
	telemetry.GraphQLRatePaused.Set(0)

	// Track auth health for the daemon's restart-to-refresh escalation.
	// authErr latches on the FIRST poll (mine or any team) that fails with an
	// auth-invalid error; anySuccess latches on the first poll that succeeds.
	// We bump the streak only on auth failure and reset it only on a real
	// success — a flapping network (transient errors, no successes) neither
	// escalates nor masks a sustained auth failure.
	authErr := false
	anySuccess := false

	// MINE: one cross-repo query.
	start := e.deps.Now()
	mineRes, mineErr := prov.FingerprintPRs(ctx, buildMineQuery(cfg))
	e.recordPoll("mine", mineRes, mineErr, e.deps.Now().Sub(start))
	if mineErr != nil {
		if errors.Is(mineErr, vcs.ErrAuthInvalid) {
			authErr = true
		}
		log.Warn("mine fingerprint poll failed", "err", mineErr.Error())
	} else {
		anySuccess = true
		mineBeads := e.openBeadsForGroup(ctx, cfg.Repos, true)
		d := diffRoster(e.prevMine, mineRes.PRs, mineBeads, !mineRes.Truncated)
		for k := range d.enqueued {
			mineQ.enqueue(k)
			telemetry.FingerprintChangesTotal.WithLabelValues("mine", d.reasons[k]).Inc()
			telemetry.RefreshEnqueuedTotal.WithLabelValues("mine").Inc()
		}
		e.prevMine = d.roster
		telemetry.RefreshQueueDepth.WithLabelValues("mine").Set(float64(mineQ.depth()))
	}

	// TEAM: per repo (team_members are per-repo). Diff the FULL roster (drafts
	// included). Accumulate the new prevTeam across all repos.
	newPrevTeam := map[prKey]string{}
	for _, rcfg := range cfg.Repos {
		// The "to-review" roster is the UNION of the team-authors, review-requested,
		// reviewed-by, and per-label buckets (buildTeamQueries) — each a separate
		// poll, since GitHub cannot OR those qualifier types in one query.
		queries := buildTeamQueries(rcfg, cfg.SelfLogin)
		if len(queries) == 0 {
			continue
		}
		var results []vcs.FingerprintResult
		bucketErr := false
		for _, q := range queries {
			s := e.deps.Now()
			res, err := prov.FingerprintPRs(ctx, q)
			e.recordPoll("team", res, err, e.deps.Now().Sub(s))
			if err != nil {
				if errors.Is(err, vcs.ErrAuthInvalid) {
					authErr = true
				}
				log.Warn("team fingerprint poll failed", "repo", rcfg.Remote, "err", err.Error())
				bucketErr = true // partial data -> keep the merge incomplete (no mass-close)
				continue
			}
			anySuccess = true
			results = append(results, res)
		}
		if len(results) == 0 {
			// Every bucket failed for this repo: preserve its prev entries so we
			// don't lose change-tracking (and don't mass-close on the next success).
			for k, h := range e.prevTeam {
				if k.Repo == rcfg.Remote {
					newPrevTeam[k] = h
				}
			}
			continue
		}
		roster, merged := mergeRosters(results)
		complete := merged && !bucketErr // a dropped bucket = partial data
		repoPrev := map[prKey]string{}
		for k, h := range e.prevTeam {
			if k.Repo == rcfg.Remote {
				repoPrev[k] = h
			}
		}
		repoBeads := e.openBeadsForGroup(ctx, []config.RepoConfig{rcfg}, false)
		d := diffRoster(repoPrev, roster, repoBeads, complete)
		for k := range d.enqueued {
			teamQ.enqueue(k)
			telemetry.FingerprintChangesTotal.WithLabelValues("team", d.reasons[k]).Inc()
			telemetry.RefreshEnqueuedTotal.WithLabelValues("team").Inc()
		}
		for k, h := range d.roster {
			newPrevTeam[k] = h
		}
		if !complete {
			// Partial data (a bucket errored/truncated): carry forward this repo's
			// prior roster entries not seen this tick, so a PR that was only in the
			// missing bucket isn't re-detected as new next tick.
			for k, h := range repoPrev {
				if _, ok := newPrevTeam[k]; !ok {
					newPrevTeam[k] = h
				}
			}
		}
	}
	e.prevTeam = newPrevTeam
	telemetry.RefreshQueueDepth.WithLabelValues("team").Set(float64(teamQ.depth()))

	// Update the auth-fail streak for the daemon's escalation. Escalate on
	// sustained auth failure; reset only on a genuine poll success so a
	// flapping network doesn't mask a real auth problem.
	switch {
	case authErr:
		e.authFailStreak++
		telemetry.GHAuthFailuresTotal.WithLabelValues("poll").Inc()
	case anySuccess:
		e.authFailStreak = 0
	}
}
