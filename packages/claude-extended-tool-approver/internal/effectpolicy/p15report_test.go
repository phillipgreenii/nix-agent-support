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
