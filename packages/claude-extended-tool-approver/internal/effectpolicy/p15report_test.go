package effectpolicy

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestP15ReportGitPush pins the committed P15 approve-reachability report for
// `git push` (packet tc-o14i5.3.10) and asserts it lists the minimum facts the
// packet requires: --force-with-lease, default-remote push, and
// --all/--tags/--branches.
func TestP15ReportGitPush(t *testing.T) {
	root, _ := fixture(t)
	rep, err := GenerateGitPushP15(root)
	if err != nil {
		t.Fatal(err)
	}
	permitted := map[string]bool{}
	for _, e := range rep.PermittedEntries() {
		permitted[e.Fact] = true
	}
	for _, want := range []string{
		"flag --force-with-lease",
		"(positional) no remote argument (default remote)",
		"flag --all", "flag --tags", "flag --branches",
	} {
		if !permitted[want] {
			t.Errorf("P15 report does not list %q as feeding a Permitted path", want)
		}
	}
	for _, mustNot := range []string{"flag --force", "flag -f", "flag --prune", "flag --mirror", "flag --receive-pack", "flag --exec", "flag --delete", "flag -d"} {
		if permitted[mustNot] {
			t.Errorf("P15 report lists %q as Permitted; it must not approve", mustNot)
		}
	}
	md := rep.Markdown()
	if strings.Contains(md, root) {
		t.Error("report leaks the fixture root")
	}
	compareGolden(t, filepath.Join("testdata", "p15", "git-push.md"), md)
}

// TestP15ReportHeldForms pins the committed P15 approve-reachability report
// for pg2-xu7sz (gate 5 of the operator rulings, Phillip, 2026-10-05): the
// three forms the rulings approve -- `gh pr ready --undo`, `git branch -D`,
// `git worktree remove` without --force -- and ONLY those. Every other spec
// fact of those three commands must stay non-approving.
func TestP15ReportHeldForms(t *testing.T) {
	root, _ := fixture(t)
	rep, err := GenerateHeldFormsP15(root)
	if err != nil {
		t.Fatal(err)
	}
	permitted := map[string]bool{}
	for _, e := range rep.PermittedEntries() {
		permitted[e.Probe] = true
	}
	for _, want := range []string{
		"gh pr ready --undo 12",
		"git branch -D feature",
		"git branch -D main",
		"git worktree remove .worktrees/clean",
		"git worktree remove .worktrees/dirty",
	} {
		if !permitted[want] {
			t.Errorf("P15 report does not list %q as feeding a Permitted path", want)
		}
	}
	for _, mustNot := range []string{
		"gh pr ready 12", "gh pr ready", "gh pr ready --undo=false 12", "gh pr ready -- --undo",
		"git branch -D -f feature", "git branch -Df feature", "git branch -D --force feature",
		"git branch -D -m feature other", "git branch -f main origin/main",
		"git worktree remove",
		"git worktree remove --force .worktrees/dirty", "git worktree remove -f .worktrees/ignored-only",
	} {
		if permitted[mustNot] {
			t.Errorf("P15 report lists %q as Permitted; it must not approve", mustNot)
		}
	}
	md := rep.Markdown()
	if strings.Contains(md, root) {
		t.Error("report leaks the fixture root")
	}
	compareGolden(t, filepath.Join("testdata", "p15", "pb-held-forms.md"), md)
}
