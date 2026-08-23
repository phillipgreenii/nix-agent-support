package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

func fp(repo string, n int, oid string) vcs.PRFingerprint {
	return vcs.PRFingerprint{Repo: repo, Number: n, Author: "me", State: "open", HeadOID: oid}
}

func TestDiffRoster_AddedChangedDisappeared(t *testing.T) {
	prev := map[prKey]string{
		{Repo: "o/r", Number: 1}: fingerprintHash(fp("o/r", 1, "a")),
		{Repo: "o/r", Number: 2}: fingerprintHash(fp("o/r", 2, "b")),
	}
	roster := []vcs.PRFingerprint{
		fp("o/r", 1, "a"),  // unchanged → skip
		fp("o/r", 3, "c"),  // new, no bead → added
		fp("o/r", 2, "b2"), // changed (oid) → changed
	}
	openBeads := map[prKey]bool{
		{Repo: "o/r", Number: 1}: true,
		{Repo: "o/r", Number: 2}: true,
		{Repo: "o/r", Number: 9}: true, // bead, not in roster → disappeared
	}
	d := diffRoster(prev, roster, openBeads, true /*complete*/)
	if !d.enqueued[prKey{Repo: "o/r", Number: 3}] || d.reasons[prKey{Repo: "o/r", Number: 3}] != "added" {
		t.Errorf("PR 3 should be added: %+v", d)
	}
	if !d.enqueued[prKey{Repo: "o/r", Number: 2}] || d.reasons[prKey{Repo: "o/r", Number: 2}] != "changed" {
		t.Errorf("PR 2 should be changed: %+v", d)
	}
	if !d.enqueued[prKey{Repo: "o/r", Number: 9}] || d.reasons[prKey{Repo: "o/r", Number: 9}] != "disappeared" {
		t.Errorf("bead 9 should be disappeared: %+v", d)
	}
	if d.enqueued[prKey{Repo: "o/r", Number: 1}] {
		t.Errorf("PR 1 unchanged should be skipped")
	}
}

func TestDiffRoster_TruncatedSkipsDisappeared(t *testing.T) {
	openBeads := map[prKey]bool{{Repo: "o/r", Number: 9}: true}
	d := diffRoster(map[prKey]string{}, nil, openBeads, false /*incomplete*/)
	if d.enqueued[prKey{Repo: "o/r", Number: 9}] {
		t.Error("incomplete roster must NOT enqueue disappeared (mass-close guard)")
	}
}

// countingFingerprintVCS wraps fakeFingerprintVCS to count FingerprintPRs calls
// and return a caller-supplied rate-limit result, so a test can drive the
// engine's proactive <1000-remaining safety-buffer skip without waiting for real
// quota exhaustion.
type countingFingerprintVCS struct {
	fakeFingerprintVCS
	res   vcs.FingerprintResult
	calls int
}

func (f *countingFingerprintVCS) FingerprintPRs(_ context.Context, _ string) (vcs.FingerprintResult, error) {
	f.calls++
	return f.res, nil
}

// TestFingerprintTick_PausesBelowRateBuffer proves the daemon proactively skips
// its GraphQL fingerprint poll once GitHub's rateLimit.remaining drops below the
// 1000-point reserve, until the window reset (resetAt) — reserving the bottom
// ~1000 points for direct `gh` use. The first tick observes remaining=500 with a
// FUTURE resetAt; the SECOND tick must issue no poll and raise the pause gauge.
func TestFingerprintTick_PausesBelowRateBuffer(t *testing.T) {
	telemetry.GraphQLRatePaused.Set(0)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute).Format(time.RFC3339)
	vp := &countingFingerprintVCS{
		res: vcs.FingerprintResult{RateLeft: 500, ResetAt: reset},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	// First tick: issues the poll, observes remaining=500 (< 1000) + future reset.
	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())
	afterFirst := vp.calls
	if afterFirst == 0 {
		t.Fatalf("first tick should have issued at least one poll, got %d", afterFirst)
	}
	if got := testutil.ToFloat64(telemetry.GraphQLRatePaused); got != 0 {
		t.Errorf("pause gauge should be 0 after the observing tick, got %v", got)
	}

	// Second tick: now < resetAt and remaining < 1000 → SKIP; no new poll, gauge=1.
	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())
	if vp.calls != afterFirst {
		t.Errorf("second tick must NOT issue a poll while paused: calls went %d -> %d", afterFirst, vp.calls)
	}
	if got := testutil.ToFloat64(telemetry.GraphQLRatePaused); got != 1 {
		t.Errorf("pause gauge should be 1 while self-pausing, got %v", got)
	}
}

// TestFingerprintTick_ResumesAfterReset proves the pause is self-clearing: once
// now >= resetAt the daemon issues the poll again (the fresh poll refreshes
// remaining to ~5000) and drops the pause gauge back to 0.
func TestFingerprintTick_ResumesAfterReset(t *testing.T) {
	telemetry.GraphQLRatePaused.Set(0)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	clock := now
	reset := now.Add(30 * time.Minute).Format(time.RFC3339)
	vp := &countingFingerprintVCS{
		res: vcs.FingerprintResult{RateLeft: 500, ResetAt: reset},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger()) // observes 500
	// Advance past the reset window and make the next poll report a refreshed quota.
	clock = now.Add(31 * time.Minute)
	vp.res = vcs.FingerprintResult{RateLeft: 5000, ResetAt: clock.Add(time.Hour).Format(time.RFC3339)}
	before := vp.calls
	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())
	if vp.calls <= before {
		t.Errorf("tick after resetAt must resume polling: calls %d -> %d", before, vp.calls)
	}
	if got := testutil.ToFloat64(telemetry.GraphQLRatePaused); got != 0 {
		t.Errorf("pause gauge should clear to 0 after resume, got %v", got)
	}
}

// perQueryFingerprintVCS answers each fingerprint query independently, so a test
// can truncate or fail ONE bucket (e.g. reviewed-by) while the others succeed.
// byQuery maps a query substring to that bucket's result; errQuery names the
// substring whose poll returns pollErr instead. Unmatched queries return an
// empty, untruncated result. queries records every query issued, in order.
type perQueryFingerprintVCS struct {
	fakeFingerprintVCS
	byQuery  map[string]vcs.FingerprintResult
	errQuery string
	pollErr  error
	queries  []string
}

func (f *perQueryFingerprintVCS) FingerprintPRs(_ context.Context, query string) (vcs.FingerprintResult, error) {
	f.queries = append(f.queries, query)
	if f.errQuery != "" && strings.Contains(query, f.errQuery) {
		return vcs.FingerprintResult{}, f.pollErr
	}
	for sub, res := range f.byQuery {
		if strings.Contains(query, sub) {
			return res, nil
		}
	}
	return vcs.FingerprintResult{}, nil
}

// newDetectorEngine builds an Engine over one repo with the given provider,
// zeroed prev rosters, and the supplied bead client.
func newDetectorEngine(t *testing.T, rcfg config.RepoConfig, prov VCSProvider, bd BeadClient) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{rcfg}},
		VCS:   map[string]VCSProvider{"github": prov},
		Beads: bd,
		Now:   func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	return e
}

// teamBeadClient is a BeadClient reporting one open TEAM merge-request bead
// (author is a teammate, so openBeadsForGroup files it under team). It is the
// "disappeared" candidate the mass-close guard must protect.
type teamBeadClient struct {
	repo   string
	number int
}

func (c teamBeadClient) ListMergeRequests(context.Context, bool) ([]beads.MergeRequest, error) {
	return []beads.MergeRequest{{
		ID:     "mr-1",
		Status: "open",
		Fields: beads.MergeRequestFields{Repo: c.repo, PRNumber: c.number, Author: "teammate"},
	}}, nil
}

// TestFingerprintTick_ReviewedByBucketAddsExactlyOnePoll pins the per-tick search
// COST. The baseline for one repo — mine + team-authors + review-requested +
// assignee (pg2-4dz88.11.4) + one per watch label — must rise by EXACTLY ONE
// poll once the reviewed-by bucket is added: the bucket is one more search per
// repo per tick, not one per team member or per label. Computed from the
// config rather than hardcoded so the "+1" is the assertion, not a magic total.
func TestFingerprintTick_ReviewedByBucketAddsExactlyOnePoll(t *testing.T) {
	rcfg := config.RepoConfig{
		Remote:      "o/r",
		TeamMembers: []string{"teammate"},
		WatchLabels: []string{"lbl-one", "lbl-two"},
	}
	vp := &perQueryFingerprintVCS{}
	e := newDetectorEngine(t, rcfg, vp, noopBeads{})
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	// mine + team-authors + review-requested + assignee + one per watch label.
	baseline := 1 + 1 + 1 + 1 + len(rcfg.WatchLabels)
	want := baseline + 1 // the reviewed-by bucket
	if got := len(vp.queries); got != want {
		t.Errorf("per-tick polls = %d, want %d (pre-leaf baseline %d + 1 for reviewed-by); queries=%#v",
			got, want, baseline, vp.queries)
	}
	reviewedBy := 0
	for _, q := range vp.queries {
		if strings.Contains(q, "reviewed-by:me") {
			reviewedBy++
		}
	}
	if reviewedBy != 1 {
		t.Errorf("expected exactly 1 reviewed-by poll per repo per tick, got %d: %#v", reviewedBy, vp.queries)
	}
}

// TestFingerprintTick_TruncatedReviewedByBucketSuppressesDisappeared extends the
// mass-close guard (INV-SYNC-2) to the NEW bucket: when the reviewed-by poll
// comes back TRUNCATED, the tick's merge is incomplete, so an open team bead
// absent from the roster MUST NOT be enqueued as "disappeared". A truncated
// bucket is missing PRs, not evidence that they are gone.
func TestFingerprintTick_TruncatedReviewedByBucketSuppressesDisappeared(t *testing.T) {
	gone := prKey{Repo: "o/r", Number: 99} // open bead, in NO bucket this tick
	vp := &perQueryFingerprintVCS{byQuery: map[string]vcs.FingerprintResult{
		"reviewed-by:me": {PRs: []vcs.PRFingerprint{fp("o/r", 50, "a")}, Truncated: true},
	}}
	e := newDetectorEngine(t, config.RepoConfig{Remote: "o/r", TeamMembers: []string{"teammate"}},
		vp, teamBeadClient{repo: "o/r", number: gone.Number})
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	before := testutil.ToFloat64(telemetry.FingerprintChangesTotal.WithLabelValues("team", "disappeared"))
	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	if enq[gone] {
		t.Errorf("a truncated reviewed-by bucket must NOT mass-close (INV-SYNC-2); enqueued=%+v", enq)
	}
	if after := testutil.ToFloat64(telemetry.FingerprintChangesTotal.WithLabelValues("team", "disappeared")); after != before {
		t.Errorf("truncated bucket must emit no disappeared reason: %v -> %v", before, after)
	}
	// The bucket's own PRs are still processed — truncation suppresses only the
	// disappeared inference, never the roster itself.
	if !enq[prKey{Repo: "o/r", Number: 50}] {
		t.Errorf("a truncated bucket's returned PRs must still be enqueued; enqueued=%+v", enq)
	}
}

// TestFingerprintTick_FailedReviewedByPollKeepsPriorRoster: a reviewed-by poll
// that ERRORS (not merely truncates) has the same consequence — the merge stays
// incomplete, no "disappeared" is inferred, and the repo's prior roster entries
// are carried forward so a PR that was only in the failed bucket is not
// re-detected as new on the next tick (INV-SYNC-2).
func TestFingerprintTick_FailedReviewedByPollKeepsPriorRoster(t *testing.T) {
	gone := prKey{Repo: "o/r", Number: 99}
	onlyInFailedBucket := prKey{Repo: "o/r", Number: 60}
	vp := &perQueryFingerprintVCS{
		byQuery: map[string]vcs.FingerprintResult{
			"author:teammate": {PRs: []vcs.PRFingerprint{fp("o/r", 61, "b")}},
		},
		errQuery: "reviewed-by:me",
		pollErr:  errors.New("boom"),
	}
	e := newDetectorEngine(t, config.RepoConfig{Remote: "o/r", TeamMembers: []string{"teammate"}},
		vp, teamBeadClient{repo: "o/r", number: gone.Number})
	// Prior tick knew about a PR that only the (now failing) reviewed-by bucket
	// returns; its hash must survive this tick.
	priorHash := fingerprintHash(fp("o/r", onlyInFailedBucket.Number, "z"))
	e.prevTeam = map[prKey]string{onlyInFailedBucket: priorHash}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	before := testutil.ToFloat64(telemetry.FingerprintChangesTotal.WithLabelValues("team", "disappeared"))
	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	if enq[gone] {
		t.Errorf("a failed reviewed-by poll must NOT mass-close (INV-SYNC-2); enqueued=%+v", enq)
	}
	if after := testutil.ToFloat64(telemetry.FingerprintChangesTotal.WithLabelValues("team", "disappeared")); after != before {
		t.Errorf("failed bucket must emit no disappeared reason: %v -> %v", before, after)
	}
	if got := e.prevTeam[onlyInFailedBucket]; got != priorHash {
		t.Errorf("prior roster entry for the failed bucket must be carried forward: got %q want %q", got, priorHash)
	}
	// The surviving buckets still did their job.
	if !enq[prKey{Repo: "o/r", Number: 61}] {
		t.Errorf("a succeeding bucket's PRs must still be enqueued; enqueued=%+v", enq)
	}
}

// TestFingerprintTick_CallCountFormula pins the per-tick GraphQL
// fingerprint-search call count for a config with 1 repo, 1 team member, and
// 2 watch labels (self configured) — the exact scenario named in the bead's
// cost-accounting acceptance criterion, evaluated with BOTH new buckets
// (reviewed-by, pg2-4dz88.11.2, and assignee, pg2-4dz88.11.4) present.
//
// Arithmetic (one FingerprintPRs call per query issued this tick):
//
//	mine query (always exactly 1, cross-repo)   = 1
//	team-authors bucket (TeamMembers non-empty) = 1
//	review-requested:<self> bucket              = 1
//	reviewed-by:<self> bucket                   = 1
//	assignee:<self> bucket                      = 1
//	one bucket per configured watch label (2)   = 2
//	                                       total = 7
func TestFingerprintTick_CallCountFormula(t *testing.T) {
	vp := &countingFingerprintVCS{res: vcs.FingerprintResult{RateLeft: 5000}}
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{
				{Remote: "o/r", TeamMembers: []string{"teammate"}, WatchLabels: []string{"lbl-one", "lbl-two"}},
			},
		},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	const want = 1 /* mine */ + 1 /* team-authors */ + 1 /* review-requested */ + 1 /* reviewed-by */ + 1 /* assignee */ + 2 /* watch labels */
	if vp.calls != want {
		t.Errorf("per-tick fingerprint-search calls = %d, want %d (1 repo / 1 team member / 2 watch labels, reviewed-by + assignee)", vp.calls, want)
	}
}

func TestBuildQueries(t *testing.T) {
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}, {Remote: "o/r2"}}}
	mine := buildMineQuery(cfg)
	if mine != "is:pr is:open author:me repo:o/r repo:o/r2" {
		t.Errorf("mine query = %q", mine)
	}
	team := buildTeamQuery(config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a", "b"}})
	if team != "is:pr is:open repo:o/r author:a author:b" {
		t.Errorf("team query = %q", team)
	}
	if buildTeamQuery(config.RepoConfig{Remote: "o/r"}) != "" {
		t.Error("team query with no members should be empty")
	}
}
