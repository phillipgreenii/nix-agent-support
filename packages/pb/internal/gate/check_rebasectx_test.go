package gate

// Regression tests for bead tc-xr6lj: `pb gate check` never resolved a patch-id
// gate whose commit was rebased onto a main that edited ADJACENT lines (gate
// tc-m3483). Such a rebase changes the hunk context, hence the patch-id, so the
// gate's patch-id exists in no scan range; the fallback matches the commit by the
// author date + subject recorded at `pb gate create`.
//
// git is REAL (V-3): the rebase, patch-id and author-date semantics are git's.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

type ctxRepo struct {
	dir string
	t   *testing.T
}

func (r ctxRepo) git(date string, args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(gitOnlyEnviron(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE=2026-07-01T00:00:00+00:00")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r ctxRepo) writeCommit(date, msg string, edit func(lines []string)) string {
	r.t.Helper()
	lines := strings.Split(r.git(date, "show", "HEAD:f.txt"), "\n")
	edit(lines)
	if err := os.WriteFile(filepath.Join(r.dir, "f.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(date, "commit", "-aq", "-m", msg)
	return r.git(date, "rev-parse", "HEAD")
}

const (
	featDate = "2026-06-20T10:00:00+00:00"
	featSubj = "tune five"
)

// rebasedScenario builds the tc-m3483 shape and returns the repo, c0, the ORIGINAL
// feature commit (now orphaned), the rebased commit and the final main tip.
func rebasedScenario(t *testing.T) (repo ctxRepo, c0, orig, rebased, adjacent string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	g := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-gate-rebasectx"})
	repo = ctxRepo{dir: g.Dir, t: t}
	var base strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&base, "l%d\n", i)
	}
	if _, err := g.Commit(t.Context(), "c0", map[string]string{"f.txt": base.String()}); err != nil {
		t.Fatal(err)
	}
	c0 = repo.git(featDate, "rev-parse", "HEAD")
	repo.git(featDate, "branch", "-M", "main")

	repo.git(featDate, "checkout", "-q", "-b", "feat")
	orig = repo.writeCommit(featDate, featSubj, func(l []string) { l[4] = "FIVE" })
	repo.git(featDate, "checkout", "-q", "main")
	adjacent = repo.writeCommit("2026-06-21T10:00:00+00:00", "adjacent", func(l []string) { l[2] = "THREE" })
	repo.git(featDate, "checkout", "-q", "feat")
	repo.git(featDate, "rebase", "-q", "main")
	rebased = repo.git(featDate, "rev-parse", "HEAD")
	repo.git(featDate, "checkout", "-q", "main")
	repo.git(featDate, "merge", "-q", "--ff-only", "feat")
	return repo, c0, orig, rebased, adjacent
}

func ctxGateScenario(t *testing.T, dir, gatedPatchID, baseline, appliedRef, extraMeta string, repoTail ...string) *run.FakeRunner {
	t.Helper()
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"wsid":"home","root":"/ws","terminal":"m","repos":[{"name":"repo-a","path":%q,
		"applied_ref":%q,"dirty":false%s}]}`, dir, appliedRef, strings.Join(repoTail, ""),
	)}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"}, run.Result{Stdout: fmt.Sprintf(
		`{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
		"await_id":"home:repo-a:%s","created_at":"2026-06-26T00:00:00Z",
		"metadata":{"applied_baseline":%q%s}}]}`, gatedPatchID, baseline, extraMeta,
	)}, nil)
	scriptResolvable(f)
	return f
}

func metaFor(ci patchid.CommitInfo) string {
	return fmt.Sprintf(`,"gated_sha":%q,"gated_author_date":%q,"gated_subject":%q`, ci.SHA, ci.AuthorDate, ci.Subject)
}

func gitRunner() realGitRunner { return realGitRunner{other: run.NewFakeRunner()} }

func ctxCheck(t *testing.T, f *run.FakeRunner) CheckResult {
	t.Helper()
	return preBaselineCheck(t, f)
}

// The rebase really changed the patch-id (negative control for the whole file).
func TestRebaseCtx_PatchIDActuallyChanges(t *testing.T) {
	repo, _, orig, rebased, _ := rebasedScenario(t)
	pc := patchid.Client{R: gitRunner()}
	a, err1 := pc.Compute(context.Background(), repo.dir, orig)
	b, err2 := pc.Compute(context.Background(), repo.dir, rebased)
	if err1 != nil || err2 != nil || a == b {
		t.Fatalf("expected differing patch-ids, got %q vs %q (%v, %v)", a, b, err1, err2)
	}
}

func TestRebaseCtx_ResolvesViaAuthorDateAndSubject(t *testing.T) {
	repo, c0, orig, _, _ := rebasedScenario(t)
	pc := patchid.Client{R: gitRunner()}
	pid, _ := pc.Compute(context.Background(), repo.dir, orig)
	ci, err := pc.CommitInfoOf(context.Background(), repo.dir, orig)
	if err != nil {
		t.Fatal(err)
	}
	out := ctxCheck(t, ctxGateScenario(t, repo.dir, pid, c0, "main", metaFor(ci)))
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("want g-1 resolved, got %+v", out)
	}
}

func TestRebaseCtx_UnappliedChangeStaysUnresolved(t *testing.T) {
	repo, c0, orig, _, adjacent := rebasedScenario(t)
	pc := patchid.Client{R: gitRunner()}
	pid, _ := pc.Compute(context.Background(), repo.dir, orig)
	ci, _ := pc.CommitInfoOf(context.Background(), repo.dir, orig)
	// applied_ref is the commit BEFORE the rebased one: the change is not applied.
	out := ctxCheck(t, ctxGateScenario(t, repo.dir, pid, c0, adjacent, metaFor(ci)))
	if len(out.Resolved)+len(out.WouldResolve) != 0 {
		t.Fatalf("unapplied change must not resolve, got %+v", out)
	}
}

func TestRebaseCtx_SameSubjectDifferentAuthorDateDoesNotMatch(t *testing.T) {
	repo, c0, orig, _, adjacent := rebasedScenario(t)
	pc := patchid.Client{R: gitRunner()}
	pid, _ := pc.Compute(context.Background(), repo.dir, orig)
	ci, _ := pc.CommitInfoOf(context.Background(), repo.dir, orig)
	// A decoy on main with the same subject but another author date, applied; the
	// real change is NOT in the applied range (applied_ref = decoy atop adjacent).
	repo.git(featDate, "checkout", "-q", "-B", "decoy", adjacent)
	repo.writeCommit("2026-06-22T10:00:00+00:00", featSubj, func(l []string) { l[8] = "NINE" })
	out := ctxCheck(t, ctxGateScenario(t, repo.dir, pid, c0, "decoy", metaFor(ci)))
	if len(out.Resolved)+len(out.WouldResolve) != 0 {
		t.Fatalf("a same-subject commit with a different author date must not match, got %+v", out)
	}
}

func TestRebaseCtx_LegacyGateWithoutFieldsUnchanged(t *testing.T) {
	repo, c0, orig, _, _ := rebasedScenario(t)
	pid, _ := patchid.Client{R: gitRunner()}.Compute(context.Background(), repo.dir, orig)
	out := ctxCheck(t, ctxGateScenario(t, repo.dir, pid, c0, "main", ""))
	if len(out.Resolved)+len(out.WouldResolve) != 0 {
		t.Fatalf("legacy gate (no recorded commit identity) must keep old behaviour, got %+v", out)
	}
}

// Condition 2 still applies to a fallback hit: a lock-built repo whose locked rev
// lacks the rebased commit stays Blocked.
func TestRebaseCtx_FallbackHonoursLockCondition(t *testing.T) {
	repo, c0, orig, _, adjacent := rebasedScenario(t)
	pc := patchid.Client{R: gitRunner()}
	pid, _ := pc.Compute(context.Background(), repo.dir, orig)
	ci, _ := pc.CommitInfoOf(context.Background(), repo.dir, orig)
	f := ctxGateScenario(t, repo.dir, pid, c0, "main", metaFor(ci), fmt.Sprintf(
		`,"applied_state_schema":3,"terminal_input":true,"locked_rev":%q,"overridden":false`, adjacent,
	))
	out, err := Check(context.Background(), checkDepsReal(f), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 {
		t.Fatalf("want Blocked by condition 2, got %+v", out)
	}
}
