package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// config_shared_write_test.go pins the repo-local `git config` write floor for the
// keys every worktree of a repository shares (user.name, user.email,
// protocol.<n>.allow). Incident 2026-10-07: a fixture command's `cd "$T/repo"` failed,
// and the `git config user.email agent@example.invalid` after it ran in a linked
// monorepo worktree, replacing the operator's identity in the shared `.git/config`
// for every worktree. The verdict must not depend on a preceding `cd` — the hook sees
// only the session cwd — so the repository must be PROVABLY temporary through the
// session cwd or `git -C`.

// TestConfigSharedWrite_RealRepo_Rejected: a repo-local write from a non-temp cwd is
// refused, in every spelling of "repo-local", including the exact incident shape.
func TestConfigSharedWrite_RealRepo_Rejected(t *testing.T) {
	r := New(nil)
	tests := []string{
		`git config user.email agent@example.invalid`,
		`git config user.name agent`,
		`git config --local user.email x@y.z`,
		`git config --worktree user.name x`,
		`git config set user.email x@y.z`,
		`git config USER.Email x@y.z`,
		`git config --replace-all user.email x@y.z`,
		`git config protocol.ext.allow always`,
		`git config --local protocol.file.allow always`,
		`git -C ` + realDir + ` config user.email x@y.z`,
		// The incident shape: a bare cd into a temp path, then the write. The hook
		// cannot know the cd succeeded, so it does not count as proof.
		`cd /tmp/fixture && git config user.email agent@example.invalid`,
		`cd /tmp/fixture; git config user.name agent`,
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, realDir)))
			if got.Decision != hookio.Reject {
				t.Errorf("Decision = %v (reason %q), want Reject", got.Decision, got.Reason)
			}
		})
	}
}

// TestConfigSharedWrite_ProvablyTemp_NotRejected: the same writes are fine when the
// repository is provably under a temp root — by `git -C`, or by a session cwd that
// is itself a temp dir.
func TestConfigSharedWrite_ProvablyTemp_NotRejected(t *testing.T) {
	r := New(nil)
	tmp := t.TempDir()
	tests := []struct{ name, cmd, cwd string }{
		{"-C temp from real cwd", `git -C ` + tmp + ` config user.email t@e.com`, realDir},
		{"-C temp, protocol", `git -C ` + tmp + ` config protocol.file.allow always`, realDir},
		{"temp cwd, bare", `git config user.email t@e.com`, tmp},
		{"temp cwd, --local", `git config --local user.name t`, tmp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(chdirInputNoZone(tc.cmd, tc.cwd)))
			if got.Decision == hookio.Reject {
				t.Errorf("Decision = %v (reason %q), want NOT Reject", got.Decision, got.Reason)
			}
		})
	}
}

// TestConfigSharedWrite_OtherShapes_Unchanged: everything outside the floor keeps its
// prior verdict — reads, unsets (the repair), non-repo-local scopes, and keys that are
// not shared-and-silent.
func TestConfigSharedWrite_OtherShapes_Unchanged(t *testing.T) {
	r := New(nil)
	tests := []string{
		`git config user.email`,
		`git config --get user.email`,
		`git config --unset user.email`,
		`git config --unset-all user.name`,
		`git config unset user.email`,
		`git config --global user.email x@y.z`,
		`git config --system user.email x@y.z`,
		`git config --file /tmp/x.cfg user.email x@y.z`,
		`git config -f /tmp/x.cfg user.name x`,
		`git config commit.gpgsign false`,
		`git config branch.main.remote origin`,
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, realDir)))
			if got.Decision != hookio.Approve {
				t.Errorf("Decision = %v (reason %q), want Approve", got.Decision, got.Reason)
			}
		})
	}
}
