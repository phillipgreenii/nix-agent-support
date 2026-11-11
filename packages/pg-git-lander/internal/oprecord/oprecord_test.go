package oprecord

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

type fx struct {
	t     *testing.T
	repo  *gitfixture.Repo
	clock *seamstest.Clock
	l     *locks.Layer
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	f := &fx{t: t, repo: repo, clock: seamstest.NewClock(time.Now())}
	f.l = f.layer()
	return f
}

func (f *fx) layer() *locks.Layer {
	root := f.repo.Root()
	env := append(os.Environ(), "HOME="+filepath.Join(root, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"))
	return &locks.Layer{
		Git:    &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)},
		Clock:  f.clock,
		Dir:    f.repo.Dir,
		Config: locks.Config{LeaseTTL: 30 * time.Second, Wait: 5 * time.Second},
		Sleep: func(_ context.Context, d time.Duration) error {
			f.clock.Advance(d)
			return nil
		},
		Jitter: func(max time.Duration) time.Duration { return max / 2 },
	}
}

func owner(token string) locks.Owner {
	return locks.Owner{Host: "laptop.local", PID: 41721, PIDStart: 1794132898, Token: token, ToolVersion: "26.11.07"}
}

func params() Params {
	return Params{
		Operation: "land", Branch: "feature/x", Target: "main", Owner: owner("aaaa"),
		Worktrees: []locks.WorktreeEntry{
			{Admin: "feature-x", Path: "/src/example/.worktrees/feature-x", Roles: []string{"source", "work"}, Start: locks.Start{Branch: "feature/x"}},
			{Admin: "_main", Path: "/src/example", Roles: []string{"destination"}, Start: locks.Start{Branch: "main"}},
		},
		WorkBranch:        "pg-git-lander/20261107T101500Z-3f9c2a",
		PlaceholderBranch: "pg-git-lander/idle/feature-x",
		Keep:              locks.Keep{SourceWorktree: true, SourceBranch: false, RemovalConsent: true},
		Context:           map[string]any{"request": "R-17"},
		ConfigDigest:      "sha256:abc",
	}
}

func (f *fx) read(ref string) locks.ReadState {
	f.t.Helper()
	st, err := f.l.Read(f.t.Context(), ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fx) begin() *Op {
	f.t.Helper()
	op, err := Begin(f.t.Context(), f.l, params(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func TestNewOpIDForm(t *testing.T) {
	id, err := NewOpID(time.Date(2026, 11, 7, 10, 15, 0, 0, time.UTC), strings.NewReader("\x3f\x9c\x2a"))
	if err != nil || id != "20261107T101500Z-3f9c2a" {
		t.Fatalf("%q %v", id, err)
	}
	re := regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{6}$`)
	if got, _ := NewOpID(time.Now(), nil); !re.MatchString(got) {
		t.Errorf("random id %q", got)
	}
}

func TestBeginWritesEveryFieldOfTheExampleRecord(t *testing.T) {
	f := newFx(t)
	f.begin()
	st := f.read(locks.BranchRef("feature/x"))
	if st.Rec == nil {
		t.Fatalf("no record: %v", st.Unreadable)
	}
	r := st.Rec
	if r.Schema != 1 || r.Role != locks.RoleBranch || r.State != locks.StateRunning || r.Operation != "land" ||
		r.Branch != "feature/x" || r.Target != "main" || r.Owner.Token != "aaaa" || r.Lease == nil || r.Lease.TTLSec != 30 {
		t.Errorf("header: %+v", r)
	}
	if !regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{6}$`).MatchString(r.OpID) {
		t.Errorf("op_id %q", r.OpID)
	}
	if len(r.Worktrees) != 2 || r.Worktrees[0].Roles[1] != "work" || r.Worktrees[1].Start.Branch != "main" {
		t.Errorf("worktrees: %+v", r.Worktrees)
	}
	if r.WorkBranch == "" || r.PlaceholderBranch == "" || !r.Keep.SourceWorktree || r.Keep.SourceBranch || !r.Keep.RemovalConsent {
		t.Errorf("branches/keep: %+v", r)
	}
	if r.Stage != StageLocked || r.StartedAt.IsZero() || r.StageStartedAt.IsZero() || r.ConfigDigest != "sha256:abc" {
		t.Errorf("stage/digest: %+v", r)
	}
	if r.Context["request"] != "R-17" || r.Move.Intent != nil || r.Stop != nil || r.CurrentCommand != nil {
		t.Errorf("context/move/stop: %+v", r)
	}
	// every key of the section 8.2 example is present in the blob
	var m map[string]json.RawMessage
	b := f.l.Git.Run(t.Context(), f.repo.Dir, "cat-file", "blob", st.OID)
	if err := json.Unmarshal(b.Stdout, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"schema", "role", "state", "op_id", "operation", "branch", "target", "owner", "lease", "worktrees",
		"work_branch", "placeholder_branch", "keep", "move", "started_at", "stage", "stage_started_at", "current_command",
		"tips", "attempts", "config_digest", "context", "stop",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("record lacks %q", k)
		}
	}
	// TARGET and worktree locks exist as short records of the same op
	for _, ref := range []string{locks.BranchRef("main"), locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main")} {
		if s := f.read(ref); s.Rec == nil || s.Rec.OpID != r.OpID || s.Rec.Branch != "feature/x" {
			t.Errorf("%s: %+v", ref, s.Rec)
		}
	}
}

func TestEachStageIsWrittenWithTipsResult(t *testing.T) {
	f := newFx(t)
	op := f.begin()
	for i, stage := range Stages[1:] {
		f.clock.Advance(time.Second)
		tip := strings.Repeat(string(rune('a'+i)), 40)
		if err := op.SetStage(t.Context(), stage, &tip); err != nil {
			t.Fatal(err)
		}
		r := f.read(locks.BranchRef("feature/x")).Rec
		if r.Stage != stage || r.Tips.Result == nil || *r.Tips.Result != tip {
			t.Fatalf("%s: %+v", stage, r)
		}
		if !r.StageStartedAt.After(r.StartedAt) {
			t.Errorf("%s: stage_started_at %v not after started_at %v", stage, r.StageStartedAt, r.StartedAt)
		}
	}
	want := []string{"locked", "preparing", "rebasing", "after_rebase", "before_move", "moving", "after", "cleanup"}
	if strings.Join(Stages, ",") != strings.Join(want, ",") {
		t.Errorf("stage names: %v", Stages)
	}
	// a stage write without a tip leaves tips.result alone
	if err := op.SetStage(t.Context(), StageAfter, nil); err != nil {
		t.Fatal(err)
	}
	if r := f.read(locks.BranchRef("feature/x")).Rec; r.Tips.Result == nil {
		t.Error("tips.result cleared")
	}
}

func TestMoveIntentIsWrittenBeforeTheMove(t *testing.T) {
	f := newFx(t)
	op := f.begin()
	if err := op.SetMove(t.Context(), "9a10"); err != nil {
		t.Fatal(err)
	}
	// the record on disk, read by a fresh invocation, already says moving
	r := f.read(locks.BranchRef("feature/x")).Rec
	if r.Move.Intent == nil || *r.Move.Intent != "moving" || r.Move.ExpectedResult == nil || *r.Move.ExpectedResult != "9a10" {
		t.Fatalf("move: %+v", r.Move)
	}
	// a renewal after the write does not revert it (one shared current record)
	if err := op.Branch.Renew(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := f.read(locks.BranchRef("feature/x")).Rec; r.Move.Intent == nil {
		t.Error("renewal reverted the move intent")
	}
}

func TestFieldWritersPersist(t *testing.T) {
	f := newFx(t)
	op := f.begin()
	ctx := t.Context()
	cc := &locks.CurrentCommand{Argv0: "git", PID: 7, StartedAt: f.clock.Now().UTC(), Deadline: f.clock.Now().Add(time.Minute).UTC()}
	one := "4c2e"
	for _, err := range []error{
		op.SetCurrentCommand(ctx, cc),
		op.SetAttempts(ctx, func(a *locks.Attempts) { a.ConflictRounds = 2; a.Autofix["fmt"] = 1 }),
		op.SetConfigDigest(ctx, "sha256:new"),
		op.SetTips(ctx, func(t *locks.Tips) { t.Branch = &one }),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	r := f.read(locks.BranchRef("feature/x")).Rec
	if r.CurrentCommand == nil || r.CurrentCommand.Argv0 != "git" || r.Attempts.ConflictRounds != 2 || r.Attempts.Autofix["fmt"] != 1 ||
		r.ConfigDigest != "sha256:new" || r.Tips.Branch == nil || *r.Tips.Branch != "4c2e" {
		t.Errorf("%+v", r)
	}
}

func TestStopTurnsRecordsStoppedAndDeletesTargetInOneTransaction(t *testing.T) {
	f := newFx(t)
	op := f.begin()
	ctx := t.Context()
	if err := op.SetStage(ctx, StageRebasing, nil); err != nil {
		t.Fatal(err)
	}
	if err := op.Stop(ctx, StopInfo{Reason: outcome.StopConflict, Message: "conflict in a.txt", Paths: []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	r := f.read(locks.BranchRef("feature/x")).Rec
	if r.State != locks.StateStopped || r.Lease != nil || r.Stop == nil {
		t.Fatalf("branch: %+v", r)
	}
	if r.Stop.Reason != "conflict" || r.Stop.Retryable || r.Stop.Stage != "rebasing" || r.Stop.Message != "conflict in a.txt" ||
		len(r.Stop.Paths) != 1 || r.Stop.At.IsZero() || r.Stop.MovedAlready {
		t.Errorf("stop: %+v", r.Stop)
	}
	for _, ref := range []string{locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main")} {
		if w := f.read(ref).Rec; w == nil || w.State != locks.StateStopped || w.Lease != nil || w.OpID != r.OpID {
			t.Errorf("%s: %+v", ref, w)
		}
	}
	if !f.read(locks.BranchRef("main")).Absent {
		t.Error("TARGET record not deleted")
	}
	// the held locks are finished: a late renewal cannot overwrite the stop
	if err := op.Branch.Renew(ctx); !errors.Is(err, locks.ErrLockLost) {
		t.Errorf("renew after stop: %v", err)
	}
	if f.read(locks.BranchRef("feature/x")).Rec.State != locks.StateStopped {
		t.Error("stop overwritten")
	}
}

func TestStopIsAtomicWhenARecordChangedHands(t *testing.T) {
	f := newFx(t)
	op := f.begin()
	ctx := t.Context()
	// another invocation swaps a worktree record under us
	wt := f.read(locks.WorktreeRef("_main"))
	other := *wt.Rec
	other.Owner.Token = "intruder"
	b, _ := json.Marshal(other)
	h := f.l.Git.RunInput(ctx, f.repo.Dir, b, "hash-object", "-w", "--stdin")
	f.l.Git.Run(ctx, f.repo.Dir, "update-ref", locks.WorktreeRef("_main"), strings.TrimSpace(string(h.Stdout)))
	err := op.Stop(ctx, StopInfo{Reason: outcome.StopTransient})
	if !errors.Is(err, locks.ErrLockLost) {
		t.Fatalf("err = %v", err)
	}
	// nothing was applied: BRANCH still running, TARGET still present
	if f.read(locks.BranchRef("feature/x")).Rec.State != locks.StateRunning || f.read(locks.BranchRef("main")).Absent {
		t.Error("partial stop applied")
	}
}

func (f *fx) stopped(reason outcome.StopReason) {
	f.t.Helper()
	op := f.begin()
	if err := op.SetStage(f.t.Context(), StageRebasing, nil); err != nil {
		f.t.Fatal(err)
	}
	if err := op.Stop(f.t.Context(), StopInfo{Reason: reason}); err != nil {
		f.t.Fatal(err)
	}
}

func TestTakeBackThenResumeLocksThenFinish(t *testing.T) {
	f := newFx(t)
	f.stopped(outcome.StopConflict)
	ctx := t.Context()
	res, err := TakeBack(ctx, f.l, "feature/x", owner("bbbb"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped.Stop == nil || res.Stopped.Stop.Reason != "conflict" {
		t.Errorf("stopped record: %+v", res.Stopped)
	}
	r := f.read(locks.BranchRef("feature/x")).Rec
	if r.State != locks.StateRunning || r.Owner.Token != "bbbb" || r.Lease == nil {
		t.Fatalf("taken back: %+v", r)
	}
	if err := res.TakeOtherLocks(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main"), locks.BranchRef("main")} {
		if w := f.read(ref).Rec; w == nil || w.State != locks.StateRunning || w.Owner.Token != "bbbb" || w.Lease == nil {
			t.Errorf("%s: %+v", ref, w)
		}
	}
	if err := res.Op.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{locks.BranchRef("feature/x"), locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main"), locks.BranchRef("main")} {
		if !f.read(ref).Absent {
			t.Errorf("%s left behind", ref)
		}
	}
}

func TestTwoConcurrentTakeBacksExactlyOneWins(t *testing.T) {
	f := newFx(t)
	f.stopped(outcome.StopConflict)
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = TakeBack(context.Background(), f.layer(), "feature/x", owner(string(rune('c'+i))))
		}()
	}
	wg.Wait()
	wins, lost := 0, 0
	for _, err := range results {
		var ref *locks.RefusedError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &ref) && ref.Reason == outcome.RefLostToConcurrent:
			lost++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if wins != 1 || lost != 1 {
		t.Fatalf("wins=%d lost=%d (%v)", wins, lost, results)
	}
}

func TestTakeBackRefusals(t *testing.T) {
	f := newFx(t)
	var ref *locks.RefusedError
	_, err := TakeBack(t.Context(), f.l, "feature/x", owner("x"))
	if !errors.As(err, &ref) || ref.Reason != outcome.RefNothingStopped {
		t.Fatalf("absent: %v", err)
	}
	f.begin() // running, held by a live holder
	_, err = TakeBack(t.Context(), f.l, "feature/x", owner("x"))
	if !errors.As(err, &ref) || ref.Reason != outcome.RefLostToConcurrent {
		t.Fatalf("running: %v", err)
	}
	if o := ref.Outcome("continue", "feature/x", ""); o.Exit != 5 || o.Reason != "lost-to-concurrent" {
		t.Errorf("outcome %+v", o)
	}
}

func TestBusyLocksAfterTakeBackSwapEverythingBackToStopped(t *testing.T) {
	f := newFx(t)
	f.stopped(outcome.StopConflict)
	ctx := t.Context()
	// another operation now holds TARGET (main)
	rec := NewRecord(Params{Operation: "rebase", Branch: "other", Owner: owner("zzzz")}, "OTHER-OP", f.clock.Now())
	if _, err := f.l.Acquire(ctx, locks.Spec{Ref: locks.BranchRef("main"), Role: locks.RoleBranch, Rec: rec}); err != nil {
		t.Fatal(err)
	}
	before := f.read(locks.BranchRef("feature/x"))
	res, err := TakeBack(ctx, f.l, "feature/x", owner("bbbb"))
	if err != nil {
		t.Fatal(err)
	}
	err = res.TakeOtherLocks(ctx)
	var busy *locks.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want busy", err)
	}
	if o := busy.Outcome("continue", "feature/x", "main"); o.Exit != 3 {
		t.Errorf("exit %d", o.Exit)
	}
	for _, ref := range []string{locks.BranchRef("feature/x"), locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main")} {
		w := f.read(ref).Rec
		if w == nil || w.State != locks.StateStopped || w.Lease != nil {
			t.Errorf("%s not back to stopped: %+v", ref, w)
		}
	}
	after := f.read(locks.BranchRef("feature/x"))
	if after.Rec.Stop == nil || after.Rec.Stop.Reason != "conflict" || after.Rec.Owner.Token != before.Rec.Owner.Token {
		t.Errorf("stopped record not restored: %+v", after.Rec)
	}
	// the other operation's lock is untouched
	if h := f.read(locks.BranchRef("main")).Rec; h == nil || h.OpID != "OTHER-OP" {
		t.Errorf("foreign lock disturbed: %+v", h)
	}
}

func TestQuitDeletesAllRecordsIncludingStoppedWorktreeRecords(t *testing.T) {
	f := newFx(t)
	f.stopped(outcome.StopConflict)
	ctx := t.Context()
	res, err := TakeBack(ctx, f.l, "feature/x", owner("bbbb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Quit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{locks.BranchRef("feature/x"), locks.WorktreeRef("feature-x"), locks.WorktreeRef("_main"), locks.BranchRef("main")} {
		if !f.read(ref).Absent {
			t.Errorf("%s left behind", ref)
		}
	}
}

func TestContextAndKeepFlagsSurviveAStopAndTakeBack(t *testing.T) {
	f := newFx(t)
	f.stopped(outcome.StopTransient)
	res, err := TakeBack(t.Context(), f.l, "feature/x", owner("bbbb"))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Op.Record()
	if r.Context["request"] != "R-17" || !r.Keep.SourceWorktree || r.Keep.SourceBranch || !r.Keep.RemovalConsent {
		t.Errorf("%+v %+v", r.Context, r.Keep)
	}
	if res.Stopped.Stop == nil || !res.Stopped.Stop.Retryable {
		t.Errorf("transient must be retryable: %+v", res.Stopped.Stop)
	}
}

func TestRebaseOperationTakesNoTargetLock(t *testing.T) {
	f := newFx(t)
	p := params()
	p.Operation = "rebase"
	if _, err := Begin(t.Context(), f.l, p, nil); err != nil {
		t.Fatal(err)
	}
	if !f.read(locks.BranchRef("main")).Absent {
		t.Error("rebase took a TARGET lock")
	}
}
