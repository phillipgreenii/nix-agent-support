package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// `git symbolic-ref` READ/WRITE TESTS (pg2-1k8sd).
//
// `symbolic-ref` was in NO subcommand set in this file before this bead, so every
// spelling fell through classify's terminal hookio.NotApplicable and reached `{}`
// only via chain exhaustion — measured, this worktree, 2026-08-23:
// `git symbolic-ref --short refs/remotes/origin/HEAD` alone answered `{}`, and that
// leaf is the concrete `PRIMARY="$(git config … || git symbolic-ref … || echo …)"`
// reproduction in the FF-0 landing preamble that motivated this bead. See
// symbolicRefVerdict's doc comment in git.go for the read/write operand-count
// discrimination, the `--delete` special case, and why the write form is a
// NoOpinion rather than an Ask/Reject.

// TestGit_SymbolicRefRead_Approve pins the one-operand query shape, in every flag
// combination git accepts for it, as a decisive Approve — the fix this bead exists
// for.
func TestGit_SymbolicRefRead_Approve(t *testing.T) {
	approve := []string{
		"git symbolic-ref HEAD",
		"git symbolic-ref --short HEAD",
		"git symbolic-ref -q HEAD",
		"git symbolic-ref --quiet HEAD",
		"git symbolic-ref --no-recurse HEAD",
		"git symbolic-ref -q --short HEAD",
		"git symbolic-ref --short refs/remotes/origin/HEAD", // the bead's own FF-0 reproduction
		"git symbolic-ref refs/remotes/origin/HEAD",
		// `--no-delete` is the negation of the delete flag, i.e. the delete flag is
		// OFF; it must not be mistaken for `--delete` by a prefix match.
		"git symbolic-ref --no-delete HEAD",
	}
	for _, cmd := range approve {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (one-operand symbolic-ref query)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_SymbolicRefSet_NoOpinion pins the two-operand SET shape — the write this
// bead's classify arm must NOT approve — to NoOpinion (r.refuse), not Approve, Ask,
// or Reject. See symbolicRefVerdict's doc for why NoOpinion is the justified verdict
// rather than Ask/Reject: no operator ruling establishes this as a Reject-worthy
// exfiltration/redirect class the way `git remote set-url` is, so this rule reports
// only that it examined and would not clear the leaf, matching isBranchUnsafe's and
// the `clean` arm's existing convention for a recognized-but-unruled mutation.
func TestGit_SymbolicRefSet_NoOpinion(t *testing.T) {
	noOpinion := []string{
		"git symbolic-ref HEAD refs/heads/other",
		"git symbolic-ref HEAD refs/heads/main",
		"git symbolic-ref -m reason HEAD refs/heads/other",
		`git symbolic-ref -m "reason text" HEAD refs/heads/other`,
		// the remote-tracking HEAD is the SAME ref `git remote set-head` already
		// Rejects by another porcelain — this spelling reaches it too, and today
		// gets only this weaker NoOpinion (see the asymmetry note in git.go).
		"git symbolic-ref refs/remotes/origin/HEAD refs/heads/main",
	}
	for _, cmd := range noOpinion {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (two-operand symbolic-ref SET)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_SymbolicRefDelete_NoOpinion pins the one-OPERAND-but-still-a-write
// `--delete`/`-d` shape: it names exactly one non-flag operand — the SAME count as
// a read — yet deletes the symbolic ref, so the operand count alone must not be
// trusted without checking this flag first.
func TestGit_SymbolicRefDelete_NoOpinion(t *testing.T) {
	noOpinion := []string{
		"git symbolic-ref --delete refs/remotes/origin/HEAD",
		"git symbolic-ref -d refs/remotes/origin/HEAD",
		"git symbolic-ref -qd refs/remotes/origin/HEAD", // clustered short flags
		"git symbolic-ref -dq refs/remotes/origin/HEAD",
		"git symbolic-ref --del refs/remotes/origin/HEAD", // long-flag abbreviation (pg2-os1kq class)
		"git symbolic-ref --delete -q refs/remotes/origin/HEAD",
	}
	for _, cmd := range noOpinion {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (symbolic-ref --delete is a mutation despite one operand)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_SymbolicRefMalformed_NoOpinion pins the fail-safe direction for shapes
// that are neither a clean one-operand read nor a clean two-operand set — zero
// operands, or three-plus. None of these may reach Approve.
func TestGit_SymbolicRefMalformed_NoOpinion(t *testing.T) {
	noOpinion := []string{
		"git symbolic-ref",
		"git symbolic-ref -q",
		"git symbolic-ref HEAD refs/heads/other refs/heads/extra",
	}
	for _, cmd := range noOpinion {
		got := evalCmd(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s), want a non-approving verdict for a malformed/unclear operand count", cmd, got.Reason)
		}
	}
}

// TestGit_SymbolicRef_TextIsNotAnOperation is the pg2-5b901-class text-vs-parsed
// guard, mirroring TestGit_RemoteMutation_TextIsNotAnOperation: a symbolic-ref SET
// spelling quoted inside a commit message or a `bd comment` body is TEXT and must
// never be classified as the operation it merely names.
func TestGit_SymbolicRef_TextIsNotAnOperation(t *testing.T) {
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{`git commit -m "git symbolic-ref HEAD refs/heads/evil-branch would redirect HEAD"`, hookio.Approve},
		{`bd comment pg2-1k8sd -m "git symbolic-ref --short refs/remotes/origin/HEAD used to abstain"`, hookio.NoOpinion},
	}
	for _, tc := range cases {
		got := evalCmd(t, tc.cmd)
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

// TestGit_SymbolicRefConfigInjection_StillAbstains confirms the pre-subcommand
// `-c`/`--config-env` RCE screen (hasGitConfigInjection) still fires ahead of the
// new symbolic-ref arm, exactly as it does for every other subcommand in this file.
func TestGit_SymbolicRefConfigInjection_StillAbstains(t *testing.T) {
	abstain := []string{
		`git -c core.pager="touch /tmp/pwned" symbolic-ref HEAD`,
		"git -c core.pager=EVIL symbolic-ref --short HEAD",
	}
	for _, cmd := range abstain {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain (pre-subcommand config injection screen)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_SymbolicRefGitDirRedirect_ReadStillApproves matches the existing
// read-class convention (TestGit_GitDirReadOnly_Approve): a read is a read
// regardless of GIT_DIR/GIT_WORK_TREE redirection, because symbolicRefVerdict does
// not consult the redirect env vars at all — the same choice remoteVerdict already
// makes for its own read-only Approve.
func TestGit_SymbolicRefGitDirRedirect_ReadStillApproves(t *testing.T) {
	got := evalCmd(t, "GIT_DIR=/other/.git git symbolic-ref --short HEAD")
	if got.Decision != hookio.Approve {
		t.Errorf("GIT_DIR-redirected symbolic-ref read: got %s (%s), want approve (read-class, matches remote's own read-only Approve)", got.Decision, got.Reason)
	}
}
