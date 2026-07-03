package sync

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
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
