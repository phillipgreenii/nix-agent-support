package triage

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/livestate"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
	}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoStoppedInRebase returns a canonical clone stopped in a conflicting rebase.
func repoStoppedInRebase(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "f.txt")
	git(t, dir, "init", "-q", "-b", "main")
	write(t, f, "base\n")
	git(t, dir, "add", "f.txt")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "checkout", "-q", "-b", "topic")
	write(t, f, "topic\n")
	git(t, dir, "commit", "-qam", "topic")
	git(t, dir, "checkout", "-q", "main")
	write(t, f, "main\n")
	git(t, dir, "commit", "-qam", "main")
	git(t, dir, "checkout", "-q", "topic")
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "rebase", "main")
	if err := cmd.Run(); err == nil {
		t.Fatal("rebase unexpectedly succeeded")
	}
	return dir
}

// worktreeStoppedInRebase returns a linked worktree stopped in a rebase.
func worktreeStoppedInRebase(t *testing.T) string {
	t.Helper()
	main := repoStoppedInRebase(t)
	git(t, main, "rebase", "--abort")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-q", "--detach", wt, "topic")
	cmd := exec.Command("git", "-C", wt, "-c", "user.name=t", "-c", "user.email=t@example.com", "rebase", "main")
	if err := cmd.Run(); err == nil {
		t.Fatal("rebase unexpectedly succeeded")
	}
	wt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	return wt
}

type fakeTracker struct {
	item      Item
	comments  []string
	claimErr  error
	showErr   error
	commentEr error
	calls     []string
	posted    []string
}

func (f *fakeTracker) Show(string) (Item, error) { return f.item, f.showErr }
func (f *fakeTracker) Comments(string) ([]string, error) {
	return f.comments, nil
}

func (f *fakeTracker) Claim(string, string) error {
	f.calls = append(f.calls, "claim")
	return f.claimErr
}

func (f *fakeTracker) Comment(_, text string) error {
	f.calls = append(f.calls, "comment")
	f.posted = append(f.posted, text)
	return f.commentEr
}
func (f *fakeTracker) Close(string, string) error { f.calls = append(f.calls, "close"); return nil }
func (f *fakeTracker) Release(string) error       { f.calls = append(f.calls, "release"); return nil }

func (f *fakeTracker) HandOver(string) error { f.calls = append(f.calls, "handover"); return nil }

// itemFor builds a pg-rescue item whose snapshot is the real state of dir.
func itemFor(t *testing.T, dir, kind, host string) Item {
	t.Helper()
	snap, err := livestate.Capture(dir, livestate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	return Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: map[string]any{
		MetaHost: host, MetaCwd: dir, MetaState: string(b), MetaStateKind: kind,
	}}
}

func run(t *testing.T, tr *fakeTracker) Outcome {
	t.Helper()
	out, err := Run(tr, Options{ItemID: "x-1", Actor: "a", Host: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wantCalls(t *testing.T, tr *fakeTracker, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(tr.calls, want) {
		t.Fatalf("calls = %v, want %v", tr.calls, want)
	}
}

func TestIntactWorktreeHandsOver(t *testing.T) {
	dir := worktreeStoppedInRebase(t)
	tr := &fakeTracker{item: itemFor(t, dir, KindWorktree, "h1")}
	out := run(t, tr)
	if out.Action != ActionHandover || out.Verdict != "intact" {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim", "comment", "handover")
}

func TestCanonicalCloneIsNeverActedOn(t *testing.T) {
	dir := repoStoppedInRebase(t)
	before := git(t, dir, "status", "--porcelain=v2")
	tr := &fakeTracker{item: itemFor(t, dir, KindCanonicalClone, "h1")}
	out := run(t, tr)
	if out.Action != ActionHandover || out.Verdict != "intact" || !strings.Contains(out.Reason, "canonical clone") {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim", "comment", "handover")
	if _, err := os.Stat(filepath.Join(dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("the clone's rebase was touched: %v", err)
	}
	if after := git(t, dir, "status", "--porcelain=v2"); after != before {
		t.Fatal("clone status changed")
	}
}

func TestResolvedCloses(t *testing.T) {
	dir := repoStoppedInRebase(t)
	tr := &fakeTracker{item: itemFor(t, dir, KindCanonicalClone, "h1")}
	git(t, dir, "rebase", "--abort")
	out := run(t, tr)
	if out.Action != ActionClosed || out.Verdict != "resolved" {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim", "comment", "close")
}

func TestMissingPathCloses(t *testing.T) {
	dir := worktreeStoppedInRebase(t)
	tr := &fakeTracker{item: itemFor(t, dir, KindWorktree, "h1")}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if out := run(t, tr); out.Action != ActionClosed {
		t.Fatalf("got %+v", out)
	}
}

func TestOtherHostReleases(t *testing.T) {
	dir := repoStoppedInRebase(t)
	tr := &fakeTracker{item: itemFor(t, dir, KindCanonicalClone, "other-host")}
	out := run(t, tr)
	if out.Action != ActionReleased || out.Verdict != "unreachable" {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim", "comment", "release")
}

func TestDriftedHandsOver(t *testing.T) {
	dir := worktreeStoppedInRebase(t)
	tr := &fakeTracker{item: itemFor(t, dir, KindWorktree, "h1")}
	write(t, filepath.Join(dir, "extra.txt"), "x\n")
	git(t, dir, "add", "extra.txt")
	out := run(t, tr)
	if out.Action != ActionHandover || out.Verdict != "drifted" {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim", "comment", "handover")
}

func TestUnverifiableHandsOver(t *testing.T) {
	for name, md := range map[string]map[string]any{
		"no state":     {MetaCwd: "/x", MetaStateKind: KindWorktree},
		"bad state":    {MetaCwd: "/x", MetaStateKind: KindWorktree, MetaState: "{"},
		"unknown kind": {MetaCwd: "/x", MetaStateKind: "weird", MetaState: "{}"},
		"no cwd":       {MetaStateKind: KindWorktree, MetaState: "{}"},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &fakeTracker{item: Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: md}}
			out := run(t, tr)
			if out.Action != ActionHandover || out.Verdict != "unverifiable" {
				t.Fatalf("got %+v", out)
			}
			wantCalls(t, tr, "claim", "comment", "handover")
		})
	}
}

func TestNotARepoHandsOverAndOtherHostReleases(t *testing.T) {
	md := map[string]any{MetaCwd: t.TempDir(), MetaStateKind: KindNotARepo, MetaState: `{"exists":true}`, MetaHost: "h1"}
	tr := &fakeTracker{item: Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: md}}
	if out := run(t, tr); out.Action != ActionHandover || out.Verdict != "not-a-repo" {
		t.Fatalf("got %+v", out)
	}
	md[MetaHost] = "elsewhere"
	tr = &fakeTracker{item: Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: md}}
	if out := run(t, tr); out.Action != ActionReleased {
		t.Fatalf("got %+v", out)
	}
}

func TestLatestCommentSnapshotWins(t *testing.T) {
	dir := worktreeStoppedInRebase(t)
	it := itemFor(t, dir, KindWorktree, "h1")
	// The filing-time snapshot is now stale; the dedup comment carries the current one.
	write(t, filepath.Join(dir, "extra.txt"), "x\n")
	git(t, dir, "add", "extra.txt")
	cur, err := livestate.Capture(dir, livestate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cur)
	tr := &fakeTracker{item: it, comments: []string{"noise", "body\n\nState snapshot at t (kind worktree):\n\n" + string(b) + "\n"}}
	if out := run(t, tr); out.Verdict != "intact" {
		t.Fatalf("got %+v", out)
	}
}

func TestSkippedAndPeerHeldChangeNothing(t *testing.T) {
	for name, it := range map[string]Item{
		"closed":                         {ID: "x-1", Status: "closed", Labels: []string{RescueLabel}},
		"no label":                       {ID: "x-1", Status: "open", Labels: []string{"other"}},
		"project bead, no item metadata": {ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: map[string]any{"other": "v"}},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &fakeTracker{item: it}
			if out := run(t, tr); out.Action != ActionSkipped {
				t.Fatalf("got %+v", out)
			}
			wantCalls(t, tr)
		})
	}
	tr := &fakeTracker{item: Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: map[string]any{MetaCwd: "/x"}}, claimErr: ErrPeerHeld}
	if out := run(t, tr); out.Action != ActionPeerHeld {
		t.Fatalf("got %+v", out)
	}
	wantCalls(t, tr, "claim")
}

func TestTrackerFailureReleasesClaim(t *testing.T) {
	tr := &fakeTracker{item: Item{ID: "x-1", Status: "open", Labels: []string{RescueLabel}, Metadata: map[string]any{MetaCwd: "/x"}}, commentEr: errors.New("boom")}
	if _, err := Run(tr, Options{ItemID: "x-1", Actor: "a"}); err == nil {
		t.Fatal("want error")
	}
	wantCalls(t, tr, "claim", "comment", "release")
}
