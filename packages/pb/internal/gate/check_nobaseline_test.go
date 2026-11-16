package gate

// Regression tests for bead tc-6fw03: `pb gate check` never resolved a gate whose
// applied_baseline was empty once the gated commit sat more than --last-n commits
// below applied_ref.
//
// Real incident: gates tc-ugd1k, tc-jlmfl, tc-i0ztt and tc-ha327 were created
// while their repo had no recorded applied_ref, so `pb gate create` stored
// applied_baseline "". With no baseline the scan fell back to the last 100
// commits of applied_ref; tc-ugd1k's commit was 2427 commits below it, so the gate
// aged past --stale-after and was queued for convert-to-human although the change
// had been applied. `--last-n 3000` resolved all four.
//
// git is REAL here (V-3), through the same realGitRunner as the pre-baseline
// tests: the scan semantics (`--since`, `-n`, patch-id) are git's. Only pn and bd
// are scripted.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/gittemplate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/x/gitfixture"
)

// noBaselineRepo builds base <- gated <- f1 <- f2 <- f3 <- f4 <- f5, every commit
// dated now, and returns the repo dir, the gated commit's patch-id, the sha below
// the gated commit and the tip sha.
func noBaselineRepo(t *testing.T) (dir, gatedPatchID, base, tip string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := noBaselineTemplate.Instance(t)
	return repo.Dir, noBaselineShas.gatedPatchID, noBaselineShas.base, noBaselineShas.tip
}

// noBaselineShas are the facts of the template built by noBaselineTemplate. The
// template is seeded once per test process (every commit dated then, which is
// before any test's own time.Now), and each test gets a private copy.
var noBaselineShas struct{ gatedPatchID, base, tip string }

var noBaselineTemplate = gittemplate.New("pb-gate-nobaseline", func(ctx context.Context, repo *gitfixture.Repo) error {
	commit := func(file, content, msg string) error {
		_, err := repo.Commit(ctx, msg, map[string]string{file: content})
		return err
	}
	if err := commit("base.txt", "base\n", "base"); err != nil {
		return err
	}
	base, err := revParse(ctx, repo, "HEAD")
	if err != nil {
		return err
	}
	if err := commit("gated.txt", "the gated change\n", "the gated change"); err != nil {
		return err
	}
	gated, err := revParse(ctx, repo, "HEAD")
	if err != nil {
		return err
	}
	for i := 1; i <= 5; i++ {
		if err := commit(fmt.Sprintf("f%d.txt", i), fmt.Sprintf("filler %d\n", i), fmt.Sprintf("filler %d", i)); err != nil {
			return err
		}
	}
	pid, err := patchid.Client{R: realGitRunner{other: run.NewFakeRunner()}}.Compute(ctx, repo.Dir, gated)
	if err != nil {
		return fmt.Errorf("patch-id of gated commit: %w", err)
	}
	tip, err := revParse(ctx, repo, "HEAD")
	if err != nil {
		return err
	}
	noBaselineShas.gatedPatchID, noBaselineShas.base, noBaselineShas.tip = pid, base, tip
	return nil
})

func revParse(ctx context.Context, repo *gitfixture.Repo, rev string) (string, error) {
	out, err := repo.Client.Run(ctx, "rev-parse", rev)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// noBaselineScenario scripts pn (one repo applied at tip) and bd (one gate keyed on
// gatedPatchID, created at createdAt, carrying baseline, which may be ""). resolve
// is scripted to SUCCEED, so an empty Resolved can only mean Check held the gate
// back.
func noBaselineScenario(dir, gatedPatchID, baseline, tip string, createdAt time.Time) *run.FakeRunner {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"wsid":"home","root":"/ws","terminal":"m","repos":[{"name":"repo-a","path":%q,
		"applied_ref":%q,"dirty":false}]}`, dir, tip,
	)}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
		"await_id":"home:repo-a:%s","created_at":%q,
		"metadata":{"applied_baseline":%q}}]}`, gatedPatchID, createdAt.UTC().Format(time.RFC3339), baseline,
	)}, nil)
	scriptResolvable(f)
	return f
}

func noBaselineCheck(t *testing.T, f *run.FakeRunner, lastN int, now time.Time) CheckResult {
	t.Helper()
	out, err := Check(context.Background(), checkDepsReal(f), CheckParams{
		WorkspaceDir: "/ws", LastN: lastN, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: now,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return out
}

// TestCheck_emptyBaselineGateResolvesBeyondTheLastNWindow is the tc-ugd1k
// reproduction: no baseline, the gated commit five commits below the tip, and a
// last-N window of 2 that cannot reach it. The gate MUST resolve through the scan
// anchored to its creation time.
func TestCheck_emptyBaselineGateResolvesBeyondTheLastNWindow(t *testing.T) {
	dir, pid, _, tip := noBaselineRepo(t)
	now := time.Now()
	f := noBaselineScenario(dir, pid, "", tip, now)
	out := noBaselineCheck(t, f, 2, now)
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved=%v skipped=%+v blocked=%+v stale=%+v: a gate with an empty baseline whose "+
			"commit is beyond --last-n must still resolve", out.Resolved, out.Skipped, out.Blocked, out.StaleActions)
	}
}

// TestCheck_emptyBaselineGateInsideTheWindowStillResolves pins that the widening
// does not disturb the old path: the commit is inside last-N and resolves as before.
func TestCheck_emptyBaselineGateInsideTheWindowStillResolves(t *testing.T) {
	dir, pid, _, tip := noBaselineRepo(t)
	now := time.Now()
	f := noBaselineScenario(dir, pid, "", tip, now)
	out := noBaselineCheck(t, f, 100, now)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved=%v skipped=%+v: a commit inside --last-n must resolve", out.Resolved, out.Skipped)
	}
}

// TestCheck_nonAncestorBaselineGateResolvesBeyondTheLastNWindow: the same fallback
// serves a baseline that exists but is not an ancestor of applied_ref (rewritten
// history), since the last-N form is used for both.
func TestCheck_nonAncestorBaselineGateResolvesBeyondTheLastNWindow(t *testing.T) {
	dir, pid, _, tip := noBaselineRepo(t)
	now := time.Now()
	f := noBaselineScenario(dir, pid, "0000000000000000000000000000000000000001", tip, now)
	out := noBaselineCheck(t, f, 2, now)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved=%v skipped=%+v: a non-ancestor baseline must fall back like an empty one",
			out.Resolved, out.Skipped)
	}
}

// TestCheck_emptyBaselineCommitOlderThanTheLookbackStaysPending is the negative
// control that proves the lookback is what finds the commit above: with the gate
// created more than gateLookback AFTER the commit, the --since window starts past
// it, the scan misses, and the gate stays pending (the old, documented outcome).
func TestCheck_emptyBaselineCommitOlderThanTheLookbackStaysPending(t *testing.T) {
	dir, pid, _, tip := noBaselineRepo(t)
	created := time.Now().Add(gateLookback + 24*time.Hour)
	f := noBaselineScenario(dir, pid, "", tip, created)
	out := noBaselineCheck(t, f, 2, created)
	leftAlone(t, f, out)
}

// TestCheck_emptyBaselineGateWithNoSuchPatchStaysPending: a patch-id that is nowhere
// in history is an unapplied change and must not resolve, widening or not.
func TestCheck_emptyBaselineGateWithNoSuchPatchStaysPending(t *testing.T) {
	dir, _, _, tip := noBaselineRepo(t)
	now := time.Now()
	f := noBaselineScenario(dir, "0000000000000000000000000000000000000000", "", tip, now)
	out := noBaselineCheck(t, f, 2, now)
	leftAlone(t, f, out)
}

// TestCheck_emptyBaselineWidenedHitStillHonoursTheLockCondition: finding the commit
// through the widened scan is only condition 1. For a lock-built input whose locked
// rev predates the commit the gate MUST stay Blocked.
func TestCheck_emptyBaselineWidenedHitStillHonoursTheLockCondition(t *testing.T) {
	dir, pid, base, tip := noBaselineRepo(t)
	now := time.Now()
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"wsid":"home","root":"/ws","terminal":"m","repos":[{"name":"repo-a","path":%q,
		"applied_ref":%q,"dirty":false,"applied_state_schema":3,"terminal_input":true,
		"locked_rev":%q,"overridden":false}]}`, dir, tip, base,
	)}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
		"await_id":"home:repo-a:%s","created_at":%q,"metadata":{"applied_baseline":""}}]}`,
		pid, now.UTC().Format(time.RFC3339),
	)}, nil)
	scriptResolvable(f)
	out := noBaselineCheck(t, f, 2, now)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 || out.Blocked[0].GateID != "g-1" {
		t.Fatalf("resolved=%v blocked=%+v skipped=%+v: a lock that lacks the gated commit must "+
			"keep the gate Blocked", out.Resolved, out.Blocked, out.Skipped)
	}
}
