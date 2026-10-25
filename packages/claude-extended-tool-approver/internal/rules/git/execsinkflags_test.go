package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// The argv spellings of the exec sinks the rule already screens as config
// (remote.<n>.uploadpack / remote.<n>.receivepack), and `--output` on a read-only
// subcommand (pg2-x5lf6). Measured on git 2.54.0 against a local-path remote with a
// marker script: ls-remote --upload-pack / --upload-pa / --exec, fetch --upload-pack and
// push --receive-pack / --exec each RAN the marker.

// evalIn asks the rule, wired to an evaluator rooted at projectCWD, about one command.
func evalIn(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	r := newWithProject(t)
	return hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
}

func TestGit_ExecSinkFlags_AreNotApproved(t *testing.T) {
	for _, cmd := range []string{
		"git ls-remote --upload-pack=x .",
		"git ls-remote --upload-pack x .",
		"git ls-remote --upload-pa=x .", // abbreviation
		"git ls-remote --exec=x .",      // alias of --upload-pack
		"git ls-remote -u x .",
		"git fetch --upload-pack x ../r",
		"git fetch --upload-pack=x ../r",
		"git pull --upload-pack=x ../r",
		"git push --receive-pack=x ../repo main",
		"git push --exec=x ../repo main",
		"git push --exec x ../repo main",
		"git push --receive-pac=x ../repo main", // abbreviation
	} {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — the flag names a program git runs for a local-path remote", cmd, got.Reason)
		}
	}
}

// The argv flag and its config-key twin are the same sink, so they get the SAME Decision.
func TestGit_ExecSinkFlags_MatchTheirConfigKeyTwins(t *testing.T) {
	rows := []struct{ flag, twin string }{
		{"git ls-remote --upload-pack=x origin", "git -c remote.origin.uploadpack=x ls-remote origin"},
		{"git ls-remote -u x origin", "git -c remote.origin.uploadpack=x ls-remote origin"},
		{"git fetch --upload-pack=x origin", "git -c remote.origin.uploadpack=x fetch origin"},
		{"git push --receive-pack=x origin main", "git -c remote.origin.receivepack=x push origin main"},
		{"git push --exec=x origin main", "git -c remote.origin.receivepack=x push origin main"},
	}
	for _, row := range rows {
		got, want := evalCmd(t, row.flag), evalCmd(t, row.twin)
		if got.Decision == hookio.Approve || got.Decision != want.Decision {
			t.Errorf("%q: got %s (%s); its config twin %q is %s (%s) — one sink, one decision", row.flag, got.Decision, got.Reason, row.twin, want.Decision, want.Reason)
		}
	}
}

func TestGit_ExecSinkFlags_DecisiveVerdictsAndNeighboursUnchanged(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		{"git fetch -u origin", hookio.Approve, "for fetch -u is --update-head-ok, not upload-pack"},
		{"git fetch --update-head-ok origin", hookio.Approve, "a longer flag is not upload-pack"},
		{"git ls-remote origin", hookio.Approve, "an ordinary read"},
		{"git push origin main", hookio.Approve, "an ordinary push"},
		{"git diff", hookio.Approve, "an ordinary read"},
		{"git log --oneline", hookio.Approve, "--oneline is not --output"},
		{"git push --force --receive-pack=x ../repo main", hookio.Reject, "a decisive force-push Reject must still win"},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", row.cmd, got.Decision, got.Reason, row.want, row.why)
		}
	}
}

func TestGit_OutputFlag_FollowsTheWritePathJudgment(t *testing.T) {
	notApproved := []string{
		"git diff --output=/nix/store/x",     // read-only zone
		"git log -1 --output=/etc/passwd",    // unknown zone
		"git show --output /etc/passwd HEAD", // separated value
		"git diff --outp=/etc/passwd",        // abbreviation
		"git diff --output",                  // no target
		`git diff --output="$OUT"`,           // runtime expansion
		"git diff --output=.git/hooks/post-checkout",
		"git diff-tree --output=/etc/x HEAD",
		"git diff --output=../outside.diff", // escapes the project
	}
	for _, cmd := range notApproved {
		if got := evalIn(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — --output writes an arbitrary file", cmd, got.Reason)
		}
	}
	approved := []string{
		"git diff --output=patch.diff", // writable in-project target
		"git log -1 --output patch.diff",
		"git diff --output=/tmp/patch.diff",
		"git diff --output-indicator-new=+", // a different flag
	}
	for _, cmd := range approved {
		if got := evalIn(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
		}
	}
	// Without an evaluator there is nothing to judge the target with: fail closed.
	if got := evalCmd(t, "git diff --output=patch.diff"); got.Decision == hookio.Approve {
		t.Errorf("nil evaluator: got APPROVE (%s), want fail-closed", got.Reason)
	}
}
