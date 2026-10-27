package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestGit_PoolReadVerbs pins W5 (bead pg2-b1lir, operator ruling pg2-r9kv7,
// 2026-10-27): the read-only verbs a pool session runs, including `git patch-id`,
// `git worktree list` against a read-only -C target and
// `git -c core.fsmonitor=false <read-only>`, are approved, while the verbs the
// ruling keeps out (`reset --hard`, `clean`, a worktree mutation) are not.
func TestGit_PoolReadVerbs(t *testing.T) {
	r := newWithProject(t)
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"git patch-id --stable", hookio.Approve},
		{"git -C /home/user/project patch-id", hookio.Approve},
		{"git for-each-ref --format='%(refname)' refs/heads", hookio.Approve},
		{"git ls-files -- '*.go'", hookio.Approve},
		{"git grep -n foo", hookio.Approve},
		{"git -c core.fsmonitor=false status --short", hookio.Approve},
		{"git -c core.fsmonitor=false -C /home/user/project log -1", hookio.Approve},
		// worktree list needs only a READABLE -C directory; every other verb a writable one.
		{"git -C /nix/store/abc123-foo worktree list", hookio.Approve},
		{"git -C /home/user/project worktree list --porcelain", hookio.Approve},
		{"git worktree list", hookio.Approve},
		{"git -C /nix/store/abc123-foo worktree add ../x", hookio.NoOpinion},
		{"git -C /nix/store/abc123-foo worktree remove x", hookio.NoOpinion},
		{"git -C /nix/store/abc123-foo worktree prune", hookio.NoOpinion},
		{"git -C /etc worktree list", hookio.NoOpinion},
		// the ruling keeps these out
		{"git reset --hard", hookio.NoOpinion},
		{"git -C /home/user/project reset --hard HEAD~1", hookio.NoOpinion},
		{"git clean -fd", hookio.NoOpinion},
		{"git -c core.pager=sh status", hookio.Reject},
	}
	for _, tt := range tests {
		got := hookio.Verdict(r.Evaluate(chdirInput(tt.cmd, projectCWD)))
		if got.Decision != tt.want {
			t.Errorf("%q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
		}
	}
}
