package rebase_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

type fx struct {
	t    *testing.T
	repo *gitfixture.Repo
	g    *gitexec.Git
	r    *rebase.Rebaser
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	return &fx{t: t, repo: repo, g: g, r: &rebase.Rebaser{Git: g}}
}

func (f *fx) git(args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), f.repo.Dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v: %s", args, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

// commit writes content to file on the current branch and commits it.
func (f *fx) commit(file, content, msg string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Dir, file)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", file)
	f.git("commit", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

// diverge leaves main with one commit and work (checked out) with one commit
// off the initial commit.
func (f *fx) diverge(mainFile, mainContent, workFile, workContent string) (mainTip, workTip string) {
	f.t.Helper()
	f.git("switch", "-c", "work")
	workTip = f.commit(workFile, workContent, "work")
	f.git("switch", "main")
	mainTip = f.commit(mainFile, mainContent, "main")
	f.git("switch", "work")
	return
}

func TestDecideLand(t *testing.T) {
	f := newFx(t)
	base := f.git("rev-parse", "HEAD")
	mainTip, workTip := f.diverge("m", "1\n", "w", "1\n")
	ctx := t.Context()
	cases := []struct {
		name           string
		branch, target string
		want           rebase.Path
	}{
		{"nothing to land", base, mainTip, rebase.NothingToLand},
		{"same tip", mainTip, mainTip, rebase.NothingToLand},
		{"diverged", workTip, mainTip, rebase.NeedsRebase},
		{"fast-forward", workTip, base, rebase.FastForward},
	}
	for _, c := range cases {
		got, err := f.r.DecideLand(ctx, f.repo.Dir, c.branch, c.target)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	if _, err := f.r.DecideLand(ctx, f.repo.Dir, "nonsense", mainTip); err == nil {
		t.Error("bad oid: want error")
	}
}

func TestDecideRebase(t *testing.T) {
	f := newFx(t)
	base := f.git("rev-parse", "HEAD")
	mainTip, workTip := f.diverge("m", "1\n", "w", "1\n")
	ctx := t.Context()
	if p, err := f.r.DecideRebase(ctx, f.repo.Dir, workTip, base); err != nil || p != rebase.UpToDate {
		t.Errorf("contains onto: %q %v", p, err)
	}
	if p, err := f.r.DecideRebase(ctx, f.repo.Dir, workTip, mainTip); err != nil || p != rebase.NeedsRebase {
		t.Errorf("diverged: %q %v", p, err)
	}
}

func TestRebaseFinishedReplaysAndReportsBaseMoved(t *testing.T) {
	f := newFx(t)
	mainTip, workTip := f.diverge("m", "1\n", "w", "1\n")
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil || !res.Finished {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if res.Tip != f.git("rev-parse", "HEAD") || len(res.Replayed) != 1 || res.Replayed[0] != res.Tip {
		t.Errorf("tip/replayed = %s %v", res.Tip, res.Replayed)
	}
	if res.Replayed[0] == workTip {
		t.Error("commit was not rewritten")
	}
	if !res.BaseMoved || len(res.Dropped) != 0 {
		t.Errorf("BaseMoved=%v dropped=%v", res.BaseMoved, res.Dropped)
	}
	if in, _ := f.r.InProgress(t.Context(), f.repo.Dir); in {
		t.Error("rebase still reported in progress")
	}
}

func TestRebaseOntoMergeBaseDoesNotMoveBase(t *testing.T) {
	f := newFx(t)
	base := f.git("rev-parse", "HEAD")
	f.git("switch", "-c", "work")
	f.commit("w", "1\n", "work")
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, base)
	if err != nil || !res.Finished {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if res.BaseMoved || len(res.Replayed) != 1 {
		t.Errorf("BaseMoved=%v replayed=%v", res.BaseMoved, res.Replayed)
	}
}

func TestRebaseReportsDroppedAlreadyUpstream(t *testing.T) {
	f := newFx(t)
	f.git("switch", "-c", "work")
	dup := f.commit("same", "x\n", "dup")
	f.commit("w", "1\n", "keep")
	f.git("switch", "main")
	f.commit("same", "x\n", "dup upstream")
	mainTip := f.commit("m", "1\n", "main")
	f.git("switch", "work")
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil || !res.Finished {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != dup || len(res.Replayed) != 1 {
		t.Errorf("dropped=%v replayed=%v", res.Dropped, res.Replayed)
	}
}

func TestRebaseDroppingEverythingReplaysNothing(t *testing.T) {
	f := newFx(t)
	f.git("switch", "-c", "work")
	dup := f.commit("same", "x\n", "dup")
	f.git("switch", "main")
	f.commit("same", "x\n", "dup upstream")
	mainTip := f.commit("m", "1\n", "main")
	f.git("switch", "work")
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil || !res.Finished {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(res.Replayed) != 0 || len(res.Dropped) != 1 || res.Dropped[0] != dup || res.BaseMoved {
		t.Errorf("res = %+v", res)
	}
}

func TestRebaseConflictStopReadsWorktreeStateNotBranchRef(t *testing.T) {
	f := newFx(t)
	oldTip := f.git("rev-parse", "HEAD")
	mainTip, workTip := f.diverge("c", "main\n", "c", "work\n")
	_ = oldTip
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Finished || res.Stop != nil || len(res.Conflict) != 1 || res.Conflict[0] != "c" {
		t.Fatalf("res = %+v", res)
	}
	ctx := t.Context()
	if in, err := f.r.InProgress(ctx, f.repo.Dir); err != nil || !in {
		t.Fatalf("InProgress = %v, %v", in, err)
	}
	// The ref still holds the old tip during the stop.
	if got := f.git("rev-parse", "refs/heads/work"); got != workTip {
		t.Errorf("work ref = %s, want old tip %s", got, workTip)
	}
	f.git("rebase", "--abort")
	if in, _ := f.r.InProgress(ctx, f.repo.Dir); in {
		t.Error("InProgress after abort")
	}
}

func TestInProgressInLinkedWorktree(t *testing.T) {
	f := newFx(t)
	mainTip, _ := f.diverge("c", "main\n", "c", "work\n")
	f.git("switch", "main")
	wt := filepath.Join(f.repo.Root(), "wt")
	f.git("worktree", "add", wt, "work")
	res, err := f.r.Rebase(t.Context(), wt, mainTip)
	if err != nil || len(res.Conflict) != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if in, err := f.r.InProgress(t.Context(), wt); err != nil || !in {
		t.Fatalf("linked InProgress = %v, %v", in, err)
	}
	if in, _ := f.r.InProgress(t.Context(), f.repo.Dir); in {
		t.Error("main worktree wrongly in progress")
	}
}

func TestRebasePreRebaseHookRefusalIsGitError(t *testing.T) {
	f := newFx(t)
	mainTip, _ := f.diverge("m", "1\n", "w", "1\n")
	script := "#!/bin/sh\necho 'no rebasing today' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(f.repo.Root(), "hooks", "pre-rebase"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Finished || len(res.Conflict) != 0 || res.Stop == nil || res.Stop.Reason != outcome.StopGitError {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Stop.LogTail, "no rebasing today") {
		t.Errorf("log tail = %q", res.Stop.LogTail)
	}
}

func TestStripHints(t *testing.T) {
	got := rebase.StripHints("error: boom\nhint: do this\nhint: or that\nfatal: bad\n")
	if got != "error: boom\nfatal: bad" {
		t.Fatalf("got %q", got)
	}
}

func TestAutoMergedPaths(t *testing.T) {
	out := "Auto-merging a.txt\nAuto-merging b/c.txt\nCONFLICT (content): x\nAuto-merging a.txt\n"
	got := rebase.AutoMergedPaths(out)
	if strings.Join(got, ",") != "a.txt,b/c.txt" {
		t.Fatalf("got %v", got)
	}
}

func TestRebaseReportsAutoMergedPaths(t *testing.T) {
	f := newFx(t)
	var lines strings.Builder
	for i := 1; i <= 30; i++ {
		lines.WriteString("line " + strings.Repeat("x", i) + "\n")
	}
	f.commit("big", lines.String(), "big")
	f.git("switch", "-c", "work")
	f.commit("big", strings.Replace(lines.String(), "line x\n", "line X\n", 1), "top edit")
	f.git("switch", "main")
	mainTip := f.commit("big", lines.String()+"tail\n", "bottom edit")
	f.git("switch", "work")
	res, err := f.r.Rebase(t.Context(), f.repo.Dir, mainTip)
	if err != nil || !res.Finished {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(res.AutoMerged) != 1 || res.AutoMerged[0] != "big" {
		t.Errorf("auto merged = %v", res.AutoMerged)
	}
}

func TestSettingsLines(t *testing.T) {
	f := newFx(t)
	f.git("config", "core.attributesFile", "/attrs")
	f.git("config", "merge.ours.driver", "true")
	f.git("config", "merge.theirs.driver", "cp %B %A")
	d, err := f.r.Settings(t.Context(), f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(d.Lines, "\n")
	for _, want := range []string{"core.attributesFile=/attrs", "core.hooksPath=", "merge.ours.driver=true", "merge.theirs.driver=cp %B %A"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if d.Drivers["ours"] != "true" || d.Drivers["theirs"] != "cp %B %A" {
		t.Errorf("drivers = %v", d.Drivers)
	}
}

func TestSettingsWithNothingConfigured(t *testing.T) {
	f := newFx(t)
	f.git("config", "--unset-all", "core.hooksPath")
	d, err := f.r.Settings(t.Context(), f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Drivers) != 0 || !strings.Contains(strings.Join(d.Lines, "\n"), "core.attributesFile=(unset)") {
		t.Errorf("d = %+v", d)
	}
}

func TestCheckAttrsUndefinedDriverWarns(t *testing.T) {
	f := newFx(t)
	f.commit(".gitattributes", "defined.txt merge=known\nghost.txt merge=ghost\nbuilt.txt merge=union\nplain.txt merge\n", "attrs")
	f.git("config", "merge.known.driver", "true")
	ctx := t.Context()
	d, err := f.r.Settings(ctx, f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.CheckAttrs(ctx, f.repo.Dir, d, []string{"defined.txt", "other.txt", "built.txt", "plain.txt"}); err != nil {
		t.Fatal(err)
	}
	if len(d.Undefined) != 0 || len(d.Warnings()) != 0 {
		t.Fatalf("unexpected undefined %v", d.Undefined)
	}
	if d.Attrs["defined.txt"] != "known" || d.Attrs["other.txt"] != "unspecified" {
		t.Errorf("attrs = %v", d.Attrs)
	}
	if err := f.r.CheckAttrs(ctx, f.repo.Dir, d, []string{"ghost.txt"}); err != nil {
		t.Fatal(err)
	}
	if len(d.Undefined) != 1 || d.Undefined[0] != "ghost" {
		t.Fatalf("undefined = %v", d.Undefined)
	}
	w := d.Warnings()
	if len(w) != 1 || w[0] != outcome.WarnUndefinedMergeDriver {
		t.Errorf("warnings = %v", w)
	}
	if !strings.Contains(strings.Join(d.Lines, "\n"), "check-attr merge -- ghost.txt: ghost") {
		t.Errorf("lines = %v", d.Lines)
	}
}

func TestCheckAttrsNoPathsIsNoop(t *testing.T) {
	f := newFx(t)
	d, _ := f.r.Settings(t.Context(), f.repo.Dir)
	n := len(d.Lines)
	if err := f.r.CheckAttrs(t.Context(), f.repo.Dir, d, nil); err != nil || len(d.Lines) != n {
		t.Fatalf("err=%v lines=%d", err, len(d.Lines))
	}
}
