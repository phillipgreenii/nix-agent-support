package gate

// Regression tests for bead pg2-k7dai: `pb gate check` never resolved a RE-GATE
// whose gated commit was already inside the gate's applied_baseline.
//
// Real incident: gate pg2-j2omo (pn:applied on nix-agent-support) gated commit
// 054c32bb8. It was a re-gate, created while the repo's applied_ref (2644adeec4ed)
// already CONTAINED 054c32bb8, so applied_baseline was a descendant of the gated
// commit. check scanned only baseline..applied_ref, which cannot contain a commit
// at or below the baseline, so a later apply (applied_ref f51b6076) that
// demonstrably built the change printed {"resolved":null,...} and the gate had
// to be closed by hand.
//
// git is REAL here (V-3): the scan semantics under test (`git log -p
// base..tip`, `git patch-id`, `merge-base --is-ancestor`) are git's, so these
// build a throwaway repository with x/gittest and run the production
// patchid.Client against it. Only pn and bd are scripted, since what they say is
// the input, not the subject.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// gitOnlyEnviron is the environment the code under test's git calls run with: an
// allowlist over the process environment (no GIT_DIR/GIT_INDEX_FILE/... of an
// outer commit hook can repoint them) plus config pins that skip machine-wide
// git config.
func gitOnlyEnviron() []string {
	env := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "TMPDIR", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TERM":
			env = append(env, kv)
		}
	}
	return env
}

// realGitRunner runs `git` for real in a hermetic environment and delegates every
// other command (pn, bd) to a scripted FakeRunner.
type realGitRunner struct{ other *run.FakeRunner }

func (r realGitRunner) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if name != "git" {
		return r.other.Run(ctx, name, args, opts)
	}
	if opts.Env == nil {
		opts.Env = gitOnlyEnviron()
	}
	return run.CLIRunner{}.Run(ctx, name, args, opts)
}

// checkDepsReal wires Check to REAL git (through realGitRunner) and to the
// scripted pn/bd responses in f.
func checkDepsReal(f *run.FakeRunner) CheckDeps {
	r := realGitRunner{other: f}
	return CheckDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}, Discover: stubDiscover("/ws")}
}

// preBaselineRepo builds c0 <- gated <- c2 <- c3 with distinct diffs and returns
// the repo directory, the patch-id of the gated commit and the four shas.
func preBaselineRepo(t *testing.T) (dir, gatedPatchID string, c0, gated, c2, c3 string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-gate-prebaseline"})
	sha := func(file, content, msg string) string {
		t.Helper()
		if _, err := repo.Commit(t.Context(), msg, map[string]string{file: content}); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
		out, err := repo.Client.Run(t.Context(), "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	c0 = sha("zero.txt", "zero\n", "c0")
	gated = sha("gated.txt", "the gated change\n", "the gated change")
	c2 = sha("two.txt", "two\n", "c2")
	c3 = sha("three.txt", "three\n", "c3")

	pid, err := patchid.Client{R: realGitRunner{other: run.NewFakeRunner()}}.Compute(t.Context(), repo.Dir, gated)
	if err != nil {
		t.Fatalf("patch-id of gated commit: %v", err)
	}
	return repo.Dir, pid, c0, gated, c2, c3
}

// preBaselineScenario scripts pn (one repo applied at appliedRef) and bd (one gate
// keyed on gatedPatchID carrying baseline) and returns the runner Check runs
// over. resolve is scripted to SUCCEED, so an empty Resolved can only mean Check
// held the gate back.
func preBaselineScenario(dir, gatedPatchID, baseline, appliedRef, repoJSONTail string) *run.FakeRunner {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"wsid":"home","root":"/ws","terminal":"m","repos":[{"name":"repo-a","path":%q,
		"applied_ref":%q,"dirty":false%s}]}`, dir, appliedRef, repoJSONTail,
	)}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
		"await_id":"home:repo-a:%s","created_at":"2026-06-26T00:00:00Z",
		"metadata":{"applied_baseline":%q}}]}`, gatedPatchID, baseline,
	)}, nil)
	scriptResolvable(f)
	return f
}

func preBaselineCheck(t *testing.T, f *run.FakeRunner) CheckResult {
	t.Helper()
	out, err := Check(context.Background(), checkDepsReal(f), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return out
}

// TestCheck_gatedCommitBelowBaselineResolvesAfterALaterApply is the pg2-k7dai
// reproduction: the gated commit is an ancestor of the baseline (a re-gate), and a
// later apply advanced applied_ref past the baseline. The gate MUST resolve. Without
// the pre-baseline lookup, the baseline..applied_ref scan holds only c3 and the gate
// is silently left blocked.
func TestCheck_gatedCommitBelowBaselineResolvesAfterALaterApply(t *testing.T) {
	dir, pid, _, _, c2, c3 := preBaselineRepo(t)
	f := preBaselineScenario(dir, pid, c2, c3, "")
	out := preBaselineCheck(t, f)
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved=%v skipped=%+v blocked=%+v: a gate whose commit is below its baseline "+
			"must resolve once an apply has advanced applied_ref past the baseline", out.Resolved, out.Skipped, out.Blocked)
	}
}

// TestCheck_gatedCommitBelowBaselineStaysBlockedUntilAnApplyRuns pins the other
// half: with applied_ref still AT the baseline, no apply has happened since the gate
// was created, so the gate MUST NOT resolve (and must not be reported as skipped or
// blocked either: it is simply pending).
func TestCheck_gatedCommitBelowBaselineStaysBlockedUntilAnApplyRuns(t *testing.T) {
	dir, pid, _, _, c2, _ := preBaselineRepo(t)
	f := preBaselineScenario(dir, pid, c2, c2, "")
	out := preBaselineCheck(t, f)
	leftAlone(t, f, out)
}

// TestCheck_gatedCommitBelowBaselineStillHonoursTheLockCondition: finding the
// commit below the baseline is only condition 1. For a repo the apply built
// THROUGH the terminal's lock (schema 3, not overridden), condition 2 still
// applies, and a lock that predates the commit keeps the gate Blocked.
func TestCheck_gatedCommitBelowBaselineStillHonoursTheLockCondition(t *testing.T) {
	dir, pid, c0, _, c2, c3 := preBaselineRepo(t)
	tail := fmt.Sprintf(`,"applied_state_schema":3,"terminal_input":true,"locked_rev":%q,"overridden":false`, c0)
	f := preBaselineScenario(dir, pid, c2, c3, tail)
	out := preBaselineCheck(t, f)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 || out.Blocked[0].GateID != "g-1" {
		t.Fatalf("resolved=%v blocked=%+v skipped=%+v: a lock-built input whose locked rev "+
			"lacks the gated commit must stay Blocked", out.Resolved, out.Blocked, out.Skipped)
	}
}

// TestCheck_gatedCommitNowhereInHistoryStaysBlocked: a patch-id that is in neither
// the range nor the baseline's history is a genuinely unapplied change and must not
// resolve.
func TestCheck_gatedCommitNowhereInHistoryStaysBlocked(t *testing.T) {
	dir, _, _, _, c2, c3 := preBaselineRepo(t)
	f := preBaselineScenario(dir, "0000000000000000000000000000000000000000", c2, c3, "")
	out := preBaselineCheck(t, f)
	leftAlone(t, f, out)
}
