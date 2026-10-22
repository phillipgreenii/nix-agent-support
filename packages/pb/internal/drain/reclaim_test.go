package drain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// bdRecorder runs everything for real EXCEPT `bd`, which it records (and
// optionally fails): git and wtdone are the behavior under test and must be
// the real tools; the bead tracker is outside this fixture.
type bdRecorder struct {
	inner run.Runner
	calls [][]string
	fail  bool
}

func (b *bdRecorder) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if name != "bd" {
		return b.inner.Run(ctx, name, args, opts)
	}
	b.calls = append(b.calls, append([]string{}, args...))
	if b.fail {
		return run.Result{}, errors.New("bd: simulated failure")
	}
	return run.Result{}, nil
}

// reclaimFixture isolates a bead in a real hermetic repository through the
// real Isolate and returns the repo, the worktree and a recording runner.
func reclaimFixture(t *testing.T, bead string) (repo, wt string, r *bdRecorder) {
	t.Helper()
	// The nix check provides wtdone (pb-go-tests testDeps); a bare `go test` on
	// a machine without it skips rather than reporting a false failure.
	if _, err := exec.LookPath("wtdone"); err != nil {
		t.Skip("wtdone not on PATH; the pb-go-tests nix check provides it")
	}
	repo = newRepo(t)
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: bead})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	return repo, out.Worktree, &bdRecorder{inner: run.CLIRunner{}}
}

func reclaim(t *testing.T, r run.Runner, repo, bead string) (ReclaimResult, error) {
	t.Helper()
	return Reclaim(context.Background(), r, ReclaimParams{RepoPath: repo, BeadID: bead, Actor: "sess-drain"})
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func branchPresent(t *testing.T, repo, branch string) bool {
	t.Helper()
	return strings.TrimSpace(gitTest(t, repo, "branch", "--list", branch)) != ""
}

func worktreeListed(t *testing.T, repo, wt string) bool {
	t.Helper()
	return strings.Contains(gitTest(t, repo, "worktree", "list", "--porcelain"), "worktree "+wt+"\n")
}

// A clean worktree on a zero-commit branch is removed, and the bead gets the
// clean-up note (acceptance: no worktree for that bead exists, bead carries the
// note).
func TestReclaim_cleanZeroCommitWorktreeIsRemovedAndNoted(t *testing.T) {
	repo, wt, r := reclaimFixture(t, "pg2-r1")
	res, err := reclaim(t, r, repo, "pg2-r1")
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if res.Outcome != OutcomeRemoved || res.Worktree != wt || res.Branch != "drain/pg2-r1" {
		t.Errorf("result = %+v, want removed %s on drain/pg2-r1", res, wt)
	}
	if exists(wt) {
		t.Errorf("worktree directory %s still exists", wt)
	}
	if worktreeListed(t, repo, wt) {
		t.Errorf("git still lists a worktree at %s", wt)
	}
	if branchPresent(t, repo, "drain/pg2-r1") {
		t.Error("zero-commit branch was kept; wtdone's plain -d should have removed it")
	}
	if len(r.calls) != 1 {
		t.Fatalf("bd calls = %v, want exactly one comment", r.calls)
	}
	call := r.calls[0]
	if len(call) != 5 || call[0] != "comment" || call[1] != "pg2-r1" || call[3] != "--actor" || call[4] != "sess-drain" {
		t.Fatalf("bd call = %q, want: comment pg2-r1 <note> --actor sess-drain", call)
	}
	for _, want := range []string{"clean-up pass", "no changes", "was removed", wt, "zero commits"} {
		if !strings.Contains(call[2], want) {
			t.Errorf("note %q lacks %q", call[2], want)
		}
	}
	if call[2] != res.Note {
		t.Errorf("Result.Note differs from the note written to the bead")
	}
}

// The branch is a zero-commit ancestor of a primary branch that moved on since:
// still nothing of its own, so still reclaimable (and `git branch -d` agrees).
func TestReclaim_primaryMovedOnStillZeroCommits(t *testing.T) {
	repo, wt, r := reclaimFixture(t, "pg2-r2")
	if err := os.WriteFile(filepath.Join(repo, "g.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "g.txt")
	gitTest(t, repo, "commit", "-m", "main moves on")
	res, err := reclaim(t, r, repo, "pg2-r2")
	if err != nil || res.Outcome != OutcomeRemoved {
		t.Fatalf("result = %+v, err = %v; want removed", res, err)
	}
	if exists(wt) || len(r.calls) != 1 {
		t.Errorf("worktree exists=%v, bd calls=%d; want removed and one note", exists(wt), len(r.calls))
	}
}

// A dirty worktree (untracked file, or a modified tracked file) is untouched
// and its bead is NOT given the note.
func TestReclaim_dirtyWorktreeIsUntouched(t *testing.T) {
	for name, dirty := range map[string]func(t *testing.T, wt string){
		"untracked": func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("wip\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"modified": func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, wt, r := reclaimFixture(t, "pg2-r3")
			dirty(t, wt)
			res, err := reclaim(t, r, repo, "pg2-r3")
			if err != nil {
				t.Fatalf("Reclaim: %v", err)
			}
			if res.Outcome != OutcomeKeptDirty || res.Reason == "" {
				t.Errorf("result = %+v, want kept-dirty with a reason", res)
			}
			if !exists(wt) || !worktreeListed(t, repo, wt) || !branchPresent(t, repo, "drain/pg2-r3") {
				t.Error("dirty worktree or its branch was removed")
			}
			if len(r.calls) != 0 {
				t.Errorf("bd calls = %v, want none (no note for a kept worktree)", r.calls)
			}
		})
	}
}

// A branch with commits of its own is untouched even when its tree is clean
// (the work is committed; a later session resumes it), and gets no note.
func TestReclaim_branchWithCommitsIsUntouched(t *testing.T) {
	repo, wt, r := reclaimFixture(t, "pg2-r4")
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "-C", wt, "add", "wip.txt")
	gitTest(t, repo, "-C", wt, "commit", "-m", "WIP (parked): pg2-r4")
	res, err := reclaim(t, r, repo, "pg2-r4")
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if res.Outcome != OutcomeKeptCommits || !strings.Contains(res.Reason, "1 commit") {
		t.Errorf("result = %+v, want kept-commits naming 1 commit", res)
	}
	if !exists(filepath.Join(wt, "wip.txt")) || !branchPresent(t, repo, "drain/pg2-r4") {
		t.Error("committed worktree or its branch was removed")
	}
	if len(r.calls) != 0 {
		t.Errorf("bd calls = %v, want none", r.calls)
	}
}

// Nothing has the branch checked out (never isolated, or already reclaimed): a
// quiet no-op, so a second call after a removal does not write a second note.
func TestReclaim_noWorktreeIsANoOpAndIdempotent(t *testing.T) {
	repo, _, r := reclaimFixture(t, "pg2-r5")
	if _, err := reclaim(t, r, repo, "pg2-r5"); err != nil {
		t.Fatal(err)
	}
	res, err := reclaim(t, r, repo, "pg2-r5")
	if err != nil || res.Outcome != OutcomeNone {
		t.Fatalf("second call = %+v, err = %v; want none", res, err)
	}
	if len(r.calls) != 1 {
		t.Errorf("bd calls = %d, want 1 (only the first, real removal is noted)", len(r.calls))
	}
}

// wtdone's liveness guard refuses a worktree a blocking process (a shell) is
// anchored in. Reclaim leaves everything in place, reports an error, and writes
// no note: the note would claim a removal that did not happen.
func TestReclaim_wtdoneRefusalLeavesWorktreeAndWritesNoNote(t *testing.T) {
	repo, wt, r := reclaimFixture(t, "pg2-r6")
	anchor := exec.Command("sh", "-c", "sleep 60; :")
	anchor.Dir = wt
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Process.Kill(); _ = anchor.Wait() })

	res, err := reclaim(t, r, repo, "pg2-r6")
	if err == nil {
		t.Fatalf("Reclaim succeeded with a shell anchored in the worktree: %+v", res)
	}
	if !exists(wt) || !worktreeListed(t, repo, wt) || !branchPresent(t, repo, "drain/pg2-r6") {
		t.Error("worktree or branch was removed despite wtdone's refusal")
	}
	if len(r.calls) != 0 {
		t.Errorf("bd calls = %v, want none", r.calls)
	}
}

// The removal stands when the note cannot be written; the error is typed so the
// caller can post the note itself.
func TestReclaim_noteFailureAfterRemovalIsTyped(t *testing.T) {
	repo, wt, r := reclaimFixture(t, "pg2-r7")
	r.fail = true
	res, err := reclaim(t, r, repo, "pg2-r7")
	if !errors.Is(err, ErrNoteFailed) {
		t.Fatalf("err = %v, want ErrNoteFailed", err)
	}
	if res.Outcome != OutcomeRemoved || res.Note == "" || exists(wt) {
		t.Errorf("result = %+v, worktree exists = %v; want removed with the note text to post", res, exists(wt))
	}
}

// The canonical clone is never an isolation worktree: a work branch that is the
// canonical clone's own checked-out branch is refused, not removed.
func TestReclaim_refusesTheCanonicalClone(t *testing.T) {
	repo, _, r := reclaimFixture(t, "pg2-r8")
	_, err := Reclaim(context.Background(), r, ReclaimParams{RepoPath: repo, BeadID: "pg2-r8", Branch: "main", Actor: "a"})
	if err == nil || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("err = %v, want a refusal naming the canonical clone", err)
	}
	if !exists(filepath.Join(repo, "f.txt")) || len(r.calls) != 0 {
		t.Error("canonical clone was touched or noted")
	}
}
