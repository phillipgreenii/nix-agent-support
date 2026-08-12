package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// LONG-FLAG ABBREVIATION TESTS (pg2-os1kq).
//
// git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option, so an
// EXACT-TOKEN long-flag test is bypassable by shortening the flag by one character,
// and the bypass direction is toward Approve. Measured on a binary built from main @
// 9c52f66b, 2026-07-30: `git reset --har HEAD~1` / `--ha` / `--h` all answered
// `allow`, each with the reason `git:modifying: git reset (soft) is safe` — so a HARD
// reset (measured PERFORMED by real git for all three spellings) was approved with a
// message asserting it was soft. `git rebase --interactiv` / `--intera` / `--int` /
// `--in` likewise answered `allow`, skipping the editor requirement.
//
// The tests below come in three layers, on purpose:
//
//  1. BEHAVIOURAL, per gated flag — generate every `--`-prefixed prefix of the
//     canonical name and assert the verdict. This is what actually pins the fix.
//  2. THE MECHANICAL GUARD — walk this package's git.go AST and fail any exact-token
//     long-flag test outside a named exemption, so a future author cannot silently
//     reintroduce the class. Layer 1 alone cannot do that: it only knows the flags
//     someone remembered to list.
//  3. REGRESSION — the sibling beads' verdicts, re-pinned against the wider matcher.

// evalCmd is the shared "ask the rule about one command string" helper.
func evalCmd(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return New(nil).Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
	})
}

// longFlagSpellings returns every spelling of canonical that git's parse-options
// could accept — the full name and every non-empty `--`-prefixed prefix of it.
// Shorter prefixes real git rejects as AMBIGUOUS are included deliberately: the
// matcher over-matches on purpose (cmdparse.HasLongFlagPrefix documents why that is
// the fail-safe direction), so the gate MUST hold for those too.
func longFlagSpellings(canonical string) []string {
	out := make([]string, 0, len(canonical))
	for n := 1; n <= len(canonical); n++ {
		out = append(out, "--"+canonical[:n])
	}
	return out
}

// TestGit_ResetHardAbbrev_NeverApprovedNorCalledSoft pins the severe half of
// pg2-os1kq. Two claims, and the second is the one that made this worse than a
// missing prompt: no `--hard` spelling may Approve, AND no verdict for one may carry
// a reason asserting the reset is soft — before the fix `--har` was approved with
// exactly that reason, so every later reader of the asklog saw a soft reset.
//
// THE EXPECTED VERDICT IS ABSTAIN, NOT ASK, SINCE pg2-ur9zc — operator ruling
// pg2-4yy4r item 4. Both of this test's claims are unchanged by that: Abstain is
// still not Approve, and the reason still must not call the reset soft. What the
// ruling moved is only WHO the non-approval is handed to.
func TestGit_ResetHardAbbrev_NeverApprovedNorCalledSoft(t *testing.T) {
	for _, flag := range longFlagSpellings("hard") {
		cmd := "git reset " + flag + " HEAD~1"
		got := evalCmd(t, cmd)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain — real git PERFORMS the hard reset for --hard/--har/--ha/--h (measured git 2.54.0, 2026-07-30), so no spelling may Approve", cmd, got.Decision, got.Reason)
		}
		if strings.Contains(strings.ToLower(got.Reason), "soft") {
			t.Errorf("cmd %q: reason %q claims the reset is soft — it is a HARD reset", cmd, got.Reason)
		}
	}
	// `--hard=x` is rejected by real git ("option `hard' takes no value"), so gating
	// it costs nothing and matching the glued form keeps the matcher uniform.
	if got := evalCmd(t, "git reset --har=x HEAD~1"); got.Decision != hookio.Abstain {
		t.Errorf("git reset --har=x: got %s, want abstain", got.Decision)
	}
}

// TestGit_RebaseInteractiveAbbrev_EditorRequired pins that every `--interactive`
// spelling is subject to the editor requirement, and that supplying the automated
// editor still makes each of them approvable — the requirement must not become a
// blanket refusal.
func TestGit_RebaseInteractiveAbbrev_EditorRequired(t *testing.T) {
	for _, flag := range longFlagSpellings("interactive") {
		bare := "git rebase " + flag + " HEAD~1"
		if got := evalCmd(t, bare); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain (interactive rebase requires an editor)", bare, got.Decision, got.Reason)
		}
		withEditor := `GIT_SEQUENCE_EDITOR="sed -i 's/^pick /fixup /'" git rebase ` + flag + " HEAD~1"
		if got := evalCmd(t, withEditor); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (the editor requirement is satisfied)", withEditor, got.Decision, got.Reason)
		}
	}
}

// TestGit_ForceWithLeaseAbbrev_SameCrossBranchCheck pins that every
// `--force-with-lease` spelling is routed through the SAME cross-branch check as the
// full spelling: cross-branch Rejects, same-branch-to-origin Approves, a non-origin
// named remote Asks, and a URL destination Rejects (the pg2-abb65 ordering).
//
// NOTE ON THE BEAD'S PREMISE, recorded because it is a finding: this half was
// ALREADY closed before pg2-os1kq. pg2-bohpm matched the flag with a measured
// minimum of len("force-w"), and re-measured on git 2.54.0, 2026-07-30, `--force-`
// and every shorter prefix is `error: ambiguous option` — so every spelling git
// accepts was already covered, and `git push --force-with-leas origin main:other`
// already answered `deny` on main @ 9c52f66b. The bead's `--force-with-leas origin
// main` row read `allow` because SAME-BRANCH-to-origin is approvable in the FULL
// spelling too, not because the abbreviation bypassed anything. What pg2-os1kq
// changes here is the removal of the measured bound, so the gate no longer depends on
// --force-if-includes continuing to exist.
func TestGit_ForceWithLeaseAbbrev_SameCrossBranchCheck(t *testing.T) {
	for _, flag := range longFlagSpellings("force-with-lease") {
		// A prefix at or below len("force") is also a prefix of `force`, which is the
		// blanket force-push Reject — a stricter verdict for the same operation, and
		// the correct one, so those spellings are asserted as Reject throughout.
		alsoPlainForce := len(flag)-2 <= len("force")

		cross := "git push " + flag + " origin main:other"
		if got := evalCmd(t, cross); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want deny (cross-branch lease)", cross, got.Decision, got.Reason)
		}

		same := "git push " + flag + " origin main"
		wantSame := hookio.Approve
		if alsoPlainForce {
			wantSame = hookio.Reject
		}
		if got := evalCmd(t, same); got.Decision != wantSame {
			t.Errorf("cmd %q: got %s (%s), want %s", same, got.Decision, got.Reason, wantSame)
		}

		nonOrigin := "git push " + flag + " upstream main"
		wantNonOrigin := hookio.Ask
		if alsoPlainForce {
			wantNonOrigin = hookio.Reject
		}
		if got := evalCmd(t, nonOrigin); got.Decision != wantNonOrigin {
			t.Errorf("cmd %q: got %s (%s), want %s", nonOrigin, got.Decision, got.Reason, wantNonOrigin)
		}

		url := "git push " + flag + " https://example.invalid/x.git main"
		if got := evalCmd(t, url); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want deny (network destination)", url, got.Decision, got.Reason)
		}
	}
	// The `=`-glued lease VALUE is still deliberately not read: the colon in
	// `--force-with-lease=<ref>:<oid>` separates the ref from the expected object id,
	// so this is a SAME-branch push carrying an explicit lease.
	if got := evalCmd(t, "git push --force-with-lea=main:abc123 origin main"); got.Decision != hookio.Approve {
		t.Errorf("abbreviated glued same-branch lease: got %s (%s), want allow", got.Decision, got.Reason)
	}
}

// TestGit_PushForceMirrorDeleteAbbrev_Reject pins the pg2-bohpm Rejects across every
// spelling. These verdicts do not change for any spelling git accepts; the open
// matcher additionally refuses the shorter prefixes git currently calls ambiguous,
// which is the fail-safe direction and removes the dependency on git's option table.
func TestGit_PushForceMirrorDeleteAbbrev_Reject(t *testing.T) {
	cases := []struct{ canonical, tail string }{
		{"force", "origin main"},
		{"mirror", "origin"},
		{"delete", "origin main"},
	}
	for _, c := range cases {
		for _, flag := range longFlagSpellings(c.canonical) {
			cmd := "git push " + flag + " " + c.tail
			if got := evalCmd(t, cmd); got.Decision != hookio.Reject {
				t.Errorf("cmd %q: got %s (%s), want deny", cmd, got.Decision, got.Reason)
			}
		}
	}
	// A LONGER flag must NOT collapse into a shorter canonical's gate: these keep
	// their own verdicts rather than reading as `--force` / `--delete`.
	for _, cmd := range []string{
		"git push --force-with-lease origin main", // same-branch lease: approvable
		"git push --dry-run origin main",          // not --delete
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — a longer flag must not match a shorter canonical", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_LongFlagAbbrev_RespectsEndOfOptions pins the `--` terminator across the
// three gates: after it a `--`-prefixed token is an OPERAND (a pathspec, a ref),
// which is how git reads it and how HasShortFlag / HasLongFlag already behave.
//
// This LOOSENS three verdicts relative to main @ 9c52f66b, deliberately and as the
// acceptance criteria require: the old exact-token hasFlag ignored the terminator, so
// `git reset -- --hard` answered `ask` for a command that resets nothing but a
// pathspec literally named `--hard`.
func TestGit_LongFlagAbbrev_RespectsEndOfOptions(t *testing.T) {
	for _, cmd := range []string{
		"git reset -- --hard",
		"git reset -- --har",
		"git reset -- --h",
		"git rebase -- --interactiv",
		"git push origin main -- --force-w",
		"git push origin main -- --force",
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — a token after `--` is an operand, not a flag", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_NonHardResetSpellings_Unchanged guards the other direction: widening the
// `--hard` test must not drag in the reset modes that are NOT `--hard`. `--h` is the
// shortest spelling of `--hard` and is gated; `--s`/`--m`/`--k` are other modes and
// keep their Approve.
func TestGit_NonHardResetSpellings_Unchanged(t *testing.T) {
	for _, cmd := range []string{
		"git reset HEAD~1",
		"git reset --soft HEAD~1",
		"git reset --mixed HEAD~1",
		"git reset --keep HEAD~1",
		"git reset --merge HEAD~1",
		"git reset --no-hard HEAD~1", // a negation is not the flag
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow (not a --hard spelling)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_Pg2os1kq_PinnedProbeRows pins every command VERBATIM from the bead's
// measured "CETA approves them" block — including its double spacing — with the
// corrected expected verdict, so the exact reproduction can never come back. The
// measurements are from a binary built from main @ 9c52f66b with
// permission_mode "default"; scripts/probe-pg2-os1kq.sh reproduces them.
//
// ONE ROW'S RECORDED ANNOTATION IS WRONG AND THE CORRECTION IS PINNED HERE. The bead
// reads `git push --force-with-leas origin main -> ALLOW <-- skips the cross-branch
// refspec check`. It does not: `allow` is the CORRECT verdict for that command,
// because a SAME-BRANCH --force-with-lease to origin is the post-rebase idiom
// pushVerdict deliberately approves, and the FULL spelling `git push
// --force-with-lease origin main` answered `allow` on the same binary. The
// cross-branch form of the abbreviation, `--force-with-leas origin main:other`,
// already answered `deny` before this bead — pg2-bohpm had matched the flag down to
// its measured minimum len("force-w"), and git refuses `--force-` and shorter as
// ambiguous. So the push half of this bead was already closed; what changed here is
// that the bound is gone, not the verdict.
func TestGit_Pg2os1kq_PinnedProbeRows(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		note string
	}{
		// The four reset rows read Abstain, not Ask, since pg2-ur9zc: the operator
		// ruled (pg2-4yy4r item 4) that this rule does not prompt for a hard reset.
		// The row this bead's probe was ABOUT is still pinned — none of the four may
		// Approve, which is what pg2-os1kq measured going wrong.
		{"git reset --hard HEAD~1", hookio.Abstain, "was ASK, correct for pg2-os1kq; Abstain since pg2-ur9zc"},
		{"git reset --har  HEAD~1", hookio.Abstain, "was ALLOW — destroys the working tree"},
		{"git reset --ha   HEAD~1", hookio.Abstain, "was ALLOW — destroys the working tree"},
		{"git reset --h    HEAD~1", hookio.Abstain, "was ALLOW — the shortest accepted spelling"},
		{"git push --force origin main", hookio.Reject, "was already correct"},
		{"git push --force-with-leas origin main", hookio.Approve, "allow is CORRECT: same-branch lease to origin"},
		{"git push --force-with-leas origin main:other", hookio.Reject, "cross-branch: already denied before this bead"},
		{"git rebase --interactiv", hookio.Abstain, "was ALLOW — skipped the editor requirement"},
	}
	for _, row := range rows {
		got := evalCmd(t, row.cmd)
		if got.Decision != row.want {
			t.Errorf("pinned row %q: got %s (%s), want %s [%s]", row.cmd, got.Decision, got.Reason, row.want, row.note)
		}
	}
}

// TestGit_SiblingBeadVerdicts_Unchanged re-pins one representative verdict from each
// of the five beads that landed in this file in the 24 hours before pg2-os1kq, since
// widening the long-flag matchers is exactly the kind of change that could move one
// of them without any of their own tests noticing.
func TestGit_SiblingBeadVerdicts_Unchanged(t *testing.T) {
	rows := []struct {
		bead, cmd string
		want      hookio.Decision
	}{
		{"pg2-bohpm force-push, long", "git push --force origin main", hookio.Reject},
		{"pg2-bohpm force-push, short", "git push -f origin main", hookio.Reject},
		{"pg2-bohpm force-push, cluster", "git push -fu origin main", hookio.Reject},
		{"pg2-bohpm force-push, refspec", "git push origin +main", hookio.Reject},
		{"pg2-bohpm remote-ref delete, flag", "git push --delete origin main", hookio.Reject},
		{"pg2-bohpm remote-ref delete, refspec", "git push origin :main", hookio.Reject},
		{"pg2-bohpm --mirror", "git push --mirror origin", hookio.Reject},
		{"pg2-bohpm cross-branch lease", "git push --force-with-lease origin main:other", hookio.Reject},
		{"pg2-bohpm same-branch lease", "git push --force-with-lease origin main", hookio.Approve},
		{"pg2-abb65 push to network URL", "git push https://example.invalid/x.git main", hookio.Reject},
		{"pg2-abb65 push to scp-like URL", "git push git@example.invalid:evil/x.git main", hookio.Reject},
		{"pg2-abb65 push to local path", "git push /tmp/dst.git main", hookio.Approve},
		{"pg2-abb65 --repo=<url>", "git push --repo=https://example.invalid/x.git main", hookio.Reject},
		{"pg2-8imjo git remote -v add", "git remote -v add upstream https://example.invalid/x.git", hookio.Reject},
		{"pg2-8imjo read-only git remote", "git remote -v", hookio.Approve},
		{"pg2-szadj core.hooksPath write", "git config core.hooksPath /tmp/h", hookio.Ask},
		{"pg2-szadj remote.origin.url write", "git config remote.origin.url https://evil.invalid/x.git", hookio.Reject},
		{"pg2-szadj config read", "git config --get user.email", hookio.Approve},
		{"pg2-szadj ordinary config write", "git config x y", hookio.Approve},
		{"pg2-szadj config read behind -f", "git config -f .git/config --get core.fsmonitor", hookio.Approve},
		{"pg2-szadj --unset of a gated key", "git config --unset clean.requireForce", hookio.Ask},
		{"pg2-szadj git config set form", "git config set core.hooksPath /tmp/h", hookio.Ask},
		// These three were pinned as Ask when the `clean` arm was one, and they keep
		// their PURPOSE at the new level: the arm is FLAG-BLIND, so all three must
		// agree. `--f` is the abbreviation row — if it ever diverges from bare `git
		// clean`, a long-flag test was reintroduced (pg2-u0e0c).
		{"pg2-u0e0c git clean stays flag-blind", "git clean", hookio.Abstain},
		{"pg2-u0e0c git clean -fdx", "git clean -fdx", hookio.Abstain},
		{"pg2-u0e0c git clean --f", "git clean --f", hookio.Abstain},
		// `git branch -D` was pinned here as Ask from pg2-bohpm until pg2-fkmg4's
		// operator ruling (2026-07-31) moved every UNGUARDED `git branch` spelling to
		// Abstain. The row stays, at its new verdict, because what it guards is that
		// the branch gate still FIRES — the failure it exists to catch is a fall-through
		// to modifyingSubcommands["branch"] and an APPROVE.
		{"pg2-fkmg4 branch -D", "git branch -D feat", hookio.Abstain},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("%s: cmd %q got %s (%s), want %s — a sibling bead's verdict moved", row.bead, row.cmd, got.Decision, got.Reason, row.want)
		}
	}
}

// TestGit_BranchForceDelete_PinnedProbeRows pins every command VERBATIM from
// pg2-os1kq's widening block, with the verdict it holds TODAY. Measured on git 2.54.0,
// one FRESH repo per spelling with a genuinely UNMERGED branch, 2026-07-30: each of
// the five gated rows answered `Deleted branch unmerged (was <sha>)` and the branch was
// gone, so this is DATA LOSS, not merely a policy bypass.
//
// THE LEVEL MOVED FROM Ask TO Abstain, and the note pg2-os1kq left here is now
// answered rather than open. That note read: "The verdict LEVEL is deliberately Ask,
// not Reject: the widening requires only that no spelling Approve, and re-classifying a
// force-delete upward is a separate, still-unreviewed question." The review happened —
// operator ruling 2026-07-31, pg2-4yy4r item 5, implemented by pg2-fkmg4 — and it moved
// the level DOWN, not up: every unguarded `git branch` spelling Abstains, which hands
// the verdict to Claude Code's prompt. See isBranchUnsafe for why that is not a
// weakening of this test, and for the auto-mode consequence the operator accepted.
func TestGit_BranchForceDelete_PinnedProbeRows(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		note string
	}{
		{"git branch -D foo", hookio.Abstain, "was ask before pg2-fkmg4; the level moved, the gate did not"},
		{"git branch -Df foo", hookio.Abstain, "was ALLOW before pg2-os1kq — clustered short"},
		{"git branch -fD foo", hookio.Abstain, "was ALLOW before pg2-os1kq — clustered short, reversed"},
		{"git branch --delete --force foo", hookio.Abstain, "was ALLOW before pg2-os1kq — LONG-FORM EQUIVALENT, not an abbreviation"},
		{"git branch --delet --forc foo", hookio.Abstain, "was ALLOW before pg2-os1kq — abbreviated long form"},
		{"git branch -d foo", hookio.Approve, "allow is CORRECT: -d refuses unmerged branches, so git's guard holds"},
	}
	for _, row := range rows {
		got := evalCmd(t, row.cmd)
		if got.Decision == hookio.Approve && row.want != hookio.Approve {
			t.Errorf("pinned row %q: got APPROVE (%s) — it fell through to modifyingSubcommands[\"branch\"]; this spelling force-deletes an unmerged branch, making its commits unreachable", row.cmd, got.Reason)
			continue
		}
		if got.Decision != row.want {
			t.Errorf("pinned row %q: got %s (%s), want %s [%s]", row.cmd, got.Decision, got.Reason, row.want, row.note)
		}
	}
}

// TestGit_BranchForceDelete_EverySpelling covers the force-DELETE spellings beyond the
// pinned block: every mixture of the delete and force halves across short, long,
// clustered and abbreviated forms, and flag-AFTER-operand orderings (the rule scans all
// tokens, so position must not matter).
//
// pg2-fkmg4 makes every row here reachable by TWO independent readings — the fused `-D`
// or the explicit force — since force alone is now unsafe. That redundancy is kept
// deliberately: these are the spellings MEASURED to destroy an unmerged branch, so they
// are the ones a future narrowing must not be able to quietly re-approve.
func TestGit_BranchForceDelete_EverySpelling(t *testing.T) {
	forceDelete := []string{
		// The fused short, bare and clustered in both orders.
		"git branch -D foo",
		"git branch -Df foo",
		"git branch -fD foo",
		"git branch -rD origin/foo",
		// Measured deleting: the conjunction in every mixture.
		"git branch --delete --force foo",
		"git branch --delet --forc foo",
		"git branch -d --force foo",
		"git branch --delete -f foo",
		"git branch -f --delet foo",
		"git branch -df foo", // both halves fused in ONE cluster
		"git branch -fd foo",
		// Flag AFTER the operand.
		"git branch foo -D",
		"git branch foo -Df",
		"git branch foo --delete --force",
		"git branch foo --force --delete",
		// A cluster whose value-taking letter comes after the D, so truncation keeps it.
		"git branch -Dt foo",
		"git branch -Dft foo",
		// Over-matched: git answers `ambiguous option: f` here, gated anyway (fail-safe).
		"git branch --d --f foo",
		// The glued long form of either half.
		"git branch --delete --force=x foo",
	}
	for _, cmd := range forceDelete {
		got := evalCmd(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — this force-deletes an unmerged branch, making its commits unreachable", cmd, got.Reason)
		}
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain — pg2-fkmg4's ruling defers every UNGUARDED `git branch` spelling to the prompt", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_BranchGuarded_StaysApprove is the other half of pg2-fkmg4's boundary: each
// row is a spelling where GIT'S OWN GUARD still stands, so it is not this rule's
// business and its Approve must not move.
//
// IT IS NARROWER THAN THE LIST IT REPLACES, and the removals are the point.
// TestGit_BranchNonForceDelete_StaysApprove asserted `-f other main`,
// `--force other main`, `--forc other main`, `-M old new` and `-C old new` as Approve,
// on the reasoning that a force MOVE/COPY/CREATE is "not a delete". pg2-fkmg4's ruling
// rejects that reasoning — those are exactly the guard-removed spellings, and `-M`/`-C`
// were MEASURED clobbering a branch the caller did not name (2026-07-31: `keepme` went
// bdfdb1f -> bad17ef). They are asserted as Abstain in
// TestGit_BranchUnguarded_Abstain; their absence here is the intended change, not a
// weakened test.
func TestGit_BranchGuarded_StaysApprove(t *testing.T) {
	cases := []struct{ cmd, why string }{
		// DELETE WITHOUT FORCE: git itself refuses an unmerged branch (measured:
		// "error: the branch 'unmerged' is not fully merged", branch still present).
		{"git branch -d foo", "delete-if-merged; git refuses unmerged"},
		{"git branch --delete foo", "same, long form"},
		{"git branch --delet foo", "same, abbreviated"},
		{"git branch -d foo bar", "two branches, still no force"},
		// MOVE / COPY WITHOUT FORCE: measured "fatal: a branch named 'keepme' already
		// exists" — git's guard holds, so these are the safe twins of -M / -C.
		{"git branch -m old new", "plain move; git refuses an existing target"},
		{"git branch --move old new", "same, long form"},
		{"git branch -c a b", "plain copy; same guard"},
		{"git branch --copy a b", "same, long form"},
		// CREATION WITHOUT FORCE: git refuses if the name exists.
		{"git branch new-branch", "create"},
		{"git branch new-branch origin/main", "create from a start point"},
		// Reads and ordinary use.
		{"git branch", "bare"},
		{"git branch -a", "list all"},
		{"git branch -r", "list remotes"},
		{"git branch --list", "list"},
		{"git branch -v", "verbose"},
		{"git branch -vv", "verbose with upstream"},
		{"git branch --show-current", "read"},
		{"git branch --contains HEAD", "read"},
		{"git branch --merged main", "read"},
		{"git branch --no-merged main", "read"},
		{"git branch --sort=-committerdate", "read"},
		{"git branch --set-upstream-to=origin/main foo", "upstream config, no ref rewritten"},
		{"git branch --unset-upstream foo", "same"},
		{"git branch --edit-description foo", "description only"},
		// GLUED SHORT VALUES: the letters after -u / -t are the option's VALUE, not
		// flag letters. Without branchShortFlagTokens these would falsely gate —
		// pg2-fkmg4 widened the letter set, so an upstream name now has FOUR ways to
		// manufacture a verdict rather than two.
		{"git branch -uorigin/DEV foo", "upstream value contains D"},
		{"git branch -uorigin/MAIN foo", "upstream value contains M"},
		{"git branch -uorigin/CI foo", "upstream value contains C"},
		{"git branch -udrafts/x foo", "upstream value contains d and f"},
		{"git branch -tdirect foo", "track value contains d"},
		{"git branch -uorigin/feature-docs foo", "upstream value contains f"},
		// OPERANDS are never scanned for flag letters, whatever they spell.
		{"git branch -d DEV-123", "branch name carries D"},
		{"git branch -m Cool-Feature Mint", "branch names carry C, F and M"},
		// END OF OPTIONS: after `--` a dashed token is an operand (a branch name).
		{"git branch -- -D", "`-D` after the terminator is a branch name"},
		{"git branch -- -M", "same"},
		{"git branch -- -C", "same"},
		{"git branch -- -f", "same"},
		{"git branch -- --delete --force", "operands, not flags"},
	}
	for _, c := range cases {
		if got := evalCmd(t, c.cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — %s", c.cmd, got.Decision, got.Reason, c.why)
		}
	}
}

// TestGit_BranchFlags_AreCaseSensitive is an explicit acceptance criterion: `-d`/`-D`,
// `-m`/`-M` and `-c`/`-C` differ in meaning, so the fused-short tests must not fold
// case. Asserted from both directions for each pair, so neither a ToLower nor a ToUpper
// slip can pass.
func TestGit_BranchFlags_AreCaseSensitive(t *testing.T) {
	pairs := []struct{ guarded, fused, op string }{
		{"git branch -d foo", "git branch -D foo", "delete"},
		{"git branch -m old new", "git branch -M old new", "move"},
		{"git branch -c a b", "git branch -C a b", "copy"},
	}
	for _, p := range pairs {
		if got := evalCmd(t, p.guarded); got.Decision != hookio.Approve {
			t.Errorf("%s: got %s (%s), want allow — the lowercase %s is GUARDED by git and MUST NOT read as its uppercase twin", p.guarded, got.Decision, got.Reason, p.op)
		}
		if got := evalCmd(t, p.fused); got.Decision != hookio.Abstain {
			t.Errorf("%s: got %s (%s), want abstain — the uppercase %s is that operation FUSED with --force", p.fused, got.Decision, got.Reason, p.op)
		}
	}
	// The force half is case-sensitive too: `-F` is not a `git branch` option at all.
	if got := evalCmd(t, "git branch -dF foo"); got.Decision != hookio.Approve {
		t.Errorf("git branch -dF: got %s (%s), want allow — `-F` is not --force", got.Decision, got.Reason)
	}
}

// TestGit_Pg2fkmg4_PinnedProbeRows pins every command VERBATIM from the bead's measured
// "Current state" block, with its CORRECTED expected verdict, plus the four extra
// spellings the acceptance criteria name (`--no-force`, `-d --force`, `--delete -f`, and
// a flag-after-operand ordering).
//
// THE SNAPSHOT WAS RE-MEASURED BEFORE THIS CHANGE, and four of its ten rows were
// already STALE. The bead recorded its table on 2026-07-31 while BLOCKED on pg2-os1kq;
// that bead then landed the clustered-short / long-form / abbreviation matching and
// applied it to branch force-DELETE. Re-measured against a binary built from this
// worktree's parent commit (fc41475c), with permission_mode "default" and XDG_DATA_HOME
// redirected — scripts/probe-pg2-fkmg4.sh reproduces it:
//
//	COMMAND                          BEAD SAID  ACTUALLY WAS  NOW
//	git branch -D foo                ask        ask           abstain
//	git branch -Df foo               ALLOW      ask (STALE)   abstain
//	git branch -fD foo               ALLOW      ask (STALE)   abstain
//	git branch --delete --force foo  ALLOW      ask (STALE)   abstain
//	git branch --delet --forc foo    ALLOW      ask (STALE)   abstain
//	git branch -M old new            ALLOW      allow         abstain
//	git branch -C a b                ALLOW      allow         abstain
//	git branch -d merged             allow      allow         allow
//	git branch -m old new            allow      allow         allow
//	git branch                       allow      allow         allow
//
// So the live gap this bead closed was `-M`, `-C` and every explicit-force spelling —
// NOT the four rows its own table blamed, which pg2-os1kq had already caught.
func TestGit_Pg2fkmg4_PinnedProbeRows(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		note string
	}{
		// The bead's ten verbatim rows.
		{"git branch -D foo", hookio.Abstain, "was ask; delete FUSED with force"},
		{"git branch -Df foo", hookio.Abstain, "bead said ALLOW, was already ask (pg2-os1kq)"},
		{"git branch -fD foo", hookio.Abstain, "bead said ALLOW, was already ask (pg2-os1kq)"},
		{"git branch --delete --force foo", hookio.Abstain, "bead said ALLOW, was already ask (pg2-os1kq)"},
		{"git branch --delet --forc foo", hookio.Abstain, "bead said ALLOW, was already ask (pg2-os1kq)"},
		{"git branch -M old new", hookio.Abstain, "was ALLOW — measured clobbering: keepme bdfdb1f -> bad17ef"},
		{"git branch -C a b", hookio.Abstain, "was ALLOW — measured overwriting an existing branch"},
		{"git branch -d merged", hookio.Approve, "allow is CORRECT: git refuses an unmerged branch"},
		{"git branch -m old new", hookio.Approve, "allow is CORRECT: git refuses an existing target"},
		{"git branch", hookio.Approve, "allow is CORRECT: a read"},
		// The four extra spellings the acceptance criteria name.
		{"git branch --no-force other main", hookio.Approve, "a NEGATION is not the flag — the `--no-` trap"},
		{"git branch -d --force foo", hookio.Abstain, "explicit force removes the delete guard"},
		{"git branch --delete -f foo", hookio.Abstain, "same, short force"},
		{"git branch foo -D", hookio.Abstain, "flag AFTER the operand: position must not matter"},
	}
	for _, row := range rows {
		got := evalCmd(t, row.cmd)
		if got.Decision != row.want {
			t.Errorf("pinned row %q: got %s (%s), want %s [%s]", row.cmd, got.Decision, got.Reason, row.want, row.note)
		}
	}
}

// TestGit_BranchUnguarded_Abstain is the UNSAFE half of pg2-fkmg4's classification:
// every spelling from which git's own guard has been removed, in every mixture of
// short, long, clustered, abbreviated and glued form, and in both flag orderings.
//
// The two assertions are deliberately separate. APPROVE is the defect — it is the
// verdict that let `-M` clobber a branch tip with nothing at the command to show for
// it. Anything other than ABSTAIN is a departure from the ruling, in either direction:
// an Ask would contradict the operator's own consequence acceptance just as an Approve
// contradicts the classification.
func TestGit_BranchUnguarded_Abstain(t *testing.T) {
	cases := []struct{ cmd, why string }{
		// FUSED: the guarded operation with --force baked into one letter.
		{"git branch -D foo", "delete + force, fused"},
		{"git branch -M old new", "move + force, fused; measured clobbering the target"},
		{"git branch -C a b", "copy + force, fused; measured overwriting an existing branch"},
		// FUSED, clustered with a read flag, in either position.
		{"git branch -Dv foo", "fused D in a cluster"},
		{"git branch -vM old new", "fused M later in a cluster"},
		{"git branch -rC a b", "fused C later in a cluster"},
		{"git branch -Dt foo", "the value-taking letter comes AFTER the D, so truncation keeps it"},
		{"git branch -Dft foo", "same, with the force letter too"},
		// EXPLICIT FORCE, which removes the guard from any of them — INCLUDING plain
		// creation, since `git branch -f <existing> <start>` silently MOVES the ref.
		{"git branch -f other main", "force CREATION moves an existing ref"},
		{"git branch --force other main", "same, long form"},
		{"git branch --forc other main", "same, abbreviated"},
		{"git branch --force=x other main", "same, glued value"},
		{"git branch -d --force foo", "delete, unguarded"},
		{"git branch --delete -f foo", "same, mixed spelling"},
		{"git branch -df foo", "same, one cluster"},
		{"git branch -fd foo", "same, reversed"},
		{"git branch -f --delet foo", "same, abbreviated long half"},
		{"git branch -m -f old new", "move, unguarded"},
		{"git branch --move --force old new", "same, long forms"},
		{"git branch -c -f a b", "copy, unguarded"},
		{"git branch --copy --force a b", "same, long forms"},
		{"git branch --d --f foo", "git calls `--f` ambiguous; gated anyway (fail-safe over-match)"},
		// FLAG AFTER THE OPERAND: every token is scanned, so position must not matter.
		{"git branch foo -D", "fused delete after the operand"},
		{"git branch old new -M", "fused move after the operands"},
		{"git branch other main -f", "explicit force after the operands"},
		{"git branch foo --delete --force", "long forms after the operand"},
	}
	for _, c := range cases {
		got := evalCmd(t, c.cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — %s, so git's own guard is GONE and this must not auto-approve", c.cmd, got.Reason, c.why)
			continue
		}
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain — %s (operator ruling pg2-4yy4r item 5: Abstain on any unsafe spelling)", c.cmd, got.Decision, got.Reason, c.why)
		}
	}
}

// TestGit_BranchNegations_AreNotTheFlag pins the `--no-` trap as its own acceptance
// criterion. `git branch -h` spells the long options `--[no-]force`, `--[no-]delete`,
// `--[no-]move` and `--[no-]copy`, so each negation is a REAL spelling that must not
// read as its positive form — which is what a prefix predicate looking for `--f…`
// would have done.
func TestGit_BranchNegations_AreNotTheFlag(t *testing.T) {
	for _, cmd := range []string{
		"git branch --no-force other main",
		"git branch --no-delete foo",
		"git branch --no-move old new",
		"git branch --no-copy a b",
		"git branch --no-contains HEAD",
		// A negation alongside a GUARDED operation stays guarded.
		"git branch --no-force -d foo",
		"git branch -d --no-force foo",
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — a `--no-` negation turns the flag OFF and MUST NOT be read as its positive form", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_BranchScope_OtherSubcommandsUnchanged guards pg2-fkmg4's explicit scope
// boundary. The operator stated the safe/unsafe principle generally but ruled it for
// `git branch` ONLY, so no other subcommand's verdict may move as a SIDE EFFECT of a
// `branch` edit — in particular the flag-blind `git clean` and the Rejects (`git push
// --force`, `git tag`) that the same principle would rewrite if it were widened without
// a ruling.
//
// THE TWO `git clean` ROWS ARE Abstain, NOT Ask, SINCE pg2-u0e0c. That is its OWN
// operator ruling (2026-07-30, pg2-4yy4r item 3), not the widening this test forbids —
// `git clean` keeps ONE flag-blind verdict for every spelling, and pg2-fkmg4's
// flag-reading predicate is still not reachable from it. What these rows guard is
// therefore unchanged: the two must AGREE with each other, and neither may become
// Approve.
func TestGit_BranchScope_OtherSubcommandsUnchanged(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
	}{
		// Abstain, NOT Ask, and that is NOT pg2-fkmg4 leaking: pg2-ur9zc moved this row
		// under a SEPARATE operator ruling (pg2-4yy4r item 4) — see
		// TestGit_ResetHard_Abstain and TestIntegration_GitResetHard_EmitsEmptyObject.
		// The row stays here on purpose: this guard's job is to pin that the `branch`
		// predicate is unreachable from `reset`, and it does that just as well against
		// reset's CURRENT verdict as against its old one.
		{"git reset --hard HEAD~1", hookio.Abstain},
		{"git reset --soft HEAD~1", hookio.Approve},
		{"git clean", hookio.Abstain},
		{"git clean -fdx", hookio.Abstain},
		{"git push --force origin main", hookio.Reject},
		{"git push -f origin main", hookio.Reject},
		{"git push --delete origin main", hookio.Reject},
		{"git push origin main", hookio.Approve},
		{"git rebase --interactiv", hookio.Abstain},
		{"git remote -v add upstream https://example.invalid/x.git", hookio.Reject},
		{"git remote -v", hookio.Approve},
		{"git config core.hooksPath /tmp/h", hookio.Ask},
		{"git config --get user.email", hookio.Approve},
		{"git tag v1", hookio.Reject},
		{"git log --oneline -5", hookio.Approve},
		{"git commit -m msg", hookio.Approve},
		{"git checkout -b feat", hookio.Approve},
		// The flag letters pg2-fkmg4 keys on appear on OTHER subcommands too, and the
		// branch predicate must not be reachable from them.
		{"git worktree remove -f ../wt", hookio.Approve},
		{"git stash drop", hookio.Approve},
		{"git add -f ignored.txt", hookio.Approve},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — pg2-fkmg4 is scoped to `git branch`; no other subcommand's verdict may move", row.cmd, got.Decision, got.Reason, row.want)
		}
	}
}

// TestGit_BranchUnguarded_EmitsEmptyHookOutput is the BOUNDARY-LEVEL assertion the
// acceptance criteria require, and it is not redundant with the rule-level tests above:
// asserting the internal Decision alone cannot show what Claude Code actually receives.
// Abstain is the decision that emits `{}`, and `{}` is what makes Claude Code prompt
// rather than auto-approve — so the property that matters is the OUTPUT, and it is
// asserted here on hookio.FormatOutput, the exact function
// cmd/claude-extended-tool-approver's handlePreToolUse writes to stdout.
//
// The chain-level twin lives in the engine integration suite
// (TestIntegration_BranchUnguardedEmitsEmptyHookOutput), which additionally proves no
// LATER rule in the production chain re-approves what this one declined to.
func TestGit_BranchUnguarded_EmitsEmptyHookOutput(t *testing.T) {
	for _, cmd := range []string{
		"git branch -M old new",
		"git branch -f other main",
	} {
		got := evalCmd(t, cmd)
		out := string(hookio.FormatOutput(got, nil))
		if out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {} — anything else is a DECISION handed to Claude Code, and `permissionDecision: \"allow\"` would auto-approve an unguarded `git branch`", cmd, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("cmd %q: emitted %s, which contains an allow decision", cmd, out)
		}
	}
}

// ---------------------------------------------------------------------------
// LAYER 2 — THE MECHANICAL GUARD
// ---------------------------------------------------------------------------

// exactTokenExemption names a function in git.go that MAY test a long flag by exact
// token, with the measured reason it is allowed to. The exemptions live HERE, in the
// test, rather than as markers in the source, so that adding one is a reviewable
// change to the guard itself.
type exactTokenExemption struct {
	fn        string
	mechanism string
	reason    string
}

// exactTokenExemptions is the complete list. Both entries are measured, not asserted.
var exactTokenExemptions = []exactTokenExemption{
	{
		fn:        "hasAbbrevLongFlag",
		mechanism: "cmdparse.HasLongFlag",
		reason: "this IS the bounded-abbreviation helper: it asks cmdparse.HasLongFlag once per " +
			"candidate spelling, longest first, so the exact-token call is the primitive it is built from",
	},
	{
		fn:        "hasGitConfigInjection",
		mechanism: "*",
		reason: "PRE-SUBCOMMAND options are parsed by git's own handle_options(), NOT by parse-options, " +
			"and it accepts NO abbreviation. Measured git 2.54.0, 2026-07-30: `git --git-di=<dir> log`, " +
			"`git --git=<dir> log`, `git --work-tre=<dir> log`, `git --namespac=<ns> log` and " +
			"`git --config-en=X=Y log` each answered `unknown option: …` while every full spelling " +
			"worked, so the exact-token test IS git's own parse and there is no bypass to close",
	},
}

func isExempt(fn, mechanism string) (string, bool) {
	for _, e := range exactTokenExemptions {
		if e.fn == fn && (e.mechanism == "*" || e.mechanism == mechanism) {
			return e.reason, true
		}
	}
	return "", false
}

// isLongFlagLit reports whether a Go expression is a string literal naming a long
// flag — `"--something"`. The bare end-of-options terminator `"--"` is NOT one, and
// neither is a short flag (`"-e"`, `"-D"`) or the `"-"` prefix probe.
func isLongFlagLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v := strings.Trim(lit.Value, "`\"")
	if len(v) <= 2 || !strings.HasPrefix(v, "--") {
		return "", false
	}
	return v, true
}

func selectorName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	}
	return ""
}

// TestGit_LongFlagTests_AreAbbreviationAware is the mechanical guard required by
// pg2-os1kq: it walks git.go's AST and fails any EXACT-TOKEN long-flag test outside
// the exemptions above, so a future author cannot silently reintroduce the class that
// approved `git reset --har`.
//
// It is an AST walk rather than a line regex on purpose: the offending mechanisms
// have to be attributed to the ENCLOSING FUNCTION for the exemptions to mean
// anything, and a line-based scan cannot do that. Failures name file:line and the
// literal so they are actionable.
//
// The mechanisms it recognises are every way this file could test a long flag by
// exact token: `hasFlag(args, "--x")`, a direct `cmdparse.HasLongFlag` call (the
// exact-token primitive), `==` / `!=` against a `"--x"` literal, a `case "--x"`, and
// `strings.HasPrefix(tok, "--x")`.
func TestGit_LongFlagTests_AreAbbreviationAware(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "git.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse git.go: %v", err)
	}

	report := func(fn, mechanism, lit string, pos token.Pos) {
		if reason, ok := isExempt(fn, mechanism); ok {
			t.Logf("exempt: %s in %s (%s) — %s", mechanism, fn, lit, reason)
			return
		}
		t.Errorf("%s: EXACT-TOKEN long-flag test %s on %s inside %s()\n"+
			"    git's parse-options accepts any unambiguous PREFIX of a long option, so this is "+
			"bypassable by shortening the flag by one character, toward Approve (pg2-os1kq).\n"+
			"    Use cmdparse.HasLongFlagPrefix for a boolean dangerous-flag test, or "+
			"hasAbbrevLongFlag with a MEASURED minimum where the match length or the flag's value "+
			"is load-bearing. See hasAbbrevLongFlag's doc for the rule.\n"+
			"    If the exact token is deliberate, add an entry to exactTokenExemptions with the "+
			"measurement that justifies it.",
			fset.Position(pos), mechanism, lit, fn)
	}

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fn := fd.Name.Name
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				switch callee := selectorName(node.Fun); callee {
				case "cmdparse.HasLongFlag":
					report(fn, "cmdparse.HasLongFlag", "(exact-token primitive)", node.Pos())
				case "hasFlag", "strings.HasPrefix", "strings.EqualFold":
					for _, arg := range node.Args {
						if lit, ok := isLongFlagLit(arg); ok {
							report(fn, callee, lit, node.Pos())
						}
					}
				}
			case *ast.BinaryExpr:
				if node.Op != token.EQL && node.Op != token.NEQ {
					return true
				}
				for _, side := range []ast.Expr{node.X, node.Y} {
					if lit, ok := isLongFlagLit(side); ok {
						report(fn, node.Op.String(), lit, node.Pos())
					}
				}
			case *ast.CaseClause:
				for _, e := range node.List {
					if lit, ok := isLongFlagLit(e); ok {
						report(fn, "case", lit, node.Pos())
					}
				}
			}
			return true
		})
	}
}

// TestGit_GatedLongFlags_UseTheChosenMatcher is the POSITIVE half of the guard: the
// negative walk above proves nothing tests a long flag by exact token, but it cannot
// notice a gate that was DELETED. This enumerates the long flags the git rule gates
// and pins each to the matcher hasAbbrevLongFlag's doc says it must use, so deleting
// a gate or quietly swapping its matcher fails here.
func TestGit_GatedLongFlags_UseTheChosenMatcher(t *testing.T) {
	// wantOpenPrefix: BOOLEAN dangerous-flag tests. Over-matching only makes the
	// verdict stricter, so these MUST use the unbounded cmdparse.HasLongFlagPrefix.
	wantOpenPrefix := []string{"hard", "interactive", "force", "mirror", "delete", "force-with-lease"}
	// wantMeasuredMinimum: the match's VALUE is read (`--repo=<url>` becomes the push
	// destination the gate rules on), so an over-match would attribute a value to a
	// flag git never parsed. MUST keep a measured minimum.
	wantMeasuredMinimum := []string{"repo"}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "git.go", nil, 0)
	if err != nil {
		t.Fatalf("parse git.go: %v", err)
	}

	openPrefix := map[string]bool{}
	measured := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := strings.Trim(lit.Value, "`\"")
		switch selectorName(call.Fun) {
		case "cmdparse.HasLongFlagPrefix":
			openPrefix[name] = true
		case "hasAbbrevLongFlag":
			measured[name] = true
		}
		return true
	})

	for _, flag := range wantOpenPrefix {
		if !openPrefix[flag] {
			t.Errorf("long flag %q is not tested with cmdparse.HasLongFlagPrefix in git.go — it is a boolean dangerous-flag test, so the OPEN prefix matcher is required (pg2-os1kq); was the gate deleted or its matcher swapped?", flag)
		}
	}
	for _, flag := range wantMeasuredMinimum {
		if !measured[flag] {
			t.Errorf("long flag %q is not tested with hasAbbrevLongFlag in git.go — its `=`-glued VALUE is load-bearing, so a MEASURED minimum is required and the open prefix matcher MUST NOT be used (pg2-os1kq)", flag)
		}
		if openPrefix[flag] {
			t.Errorf("long flag %q is tested with cmdparse.HasLongFlagPrefix — over-matching has no safe direction where the flag's value is read; use hasAbbrevLongFlag with a measured minimum (pg2-os1kq)", flag)
		}
	}
	// `git config`'s options must NOT move to the open matcher: a match there ELIDES
	// a token and so shifts the operand count configIsRead's read/write bound and
	// gatedConfigKey's key scan both depend on.
	for flag := range configWriteFlags {
		if openPrefix[flag] {
			t.Errorf("`git config` option %q is tested with cmdparse.HasLongFlagPrefix — a config-option match shifts the operand count, so an over-match could change a git config verdict; keep the measured minimum (pg2-os1kq)", flag)
		}
	}
}

// TestGit_BranchUnsafe_UsesTheRequiredMechanisms scopes the guard to isBranchUnsafe,
// because the file-wide checks above cannot tell that this predicate went missing:
// `force` is also passed to HasLongFlagPrefix by pushVerdict, so deleting the branch
// gate entirely would leave every assertion in TestGit_GatedLongFlags_UseTheChosenMatcher
// still passing while `git branch -M old new` silently returned to Approve.
//
// IT REPLACES TestGit_BranchForceDelete_UsesBothMechanisms, which pinned
// isBranchForceDelete's delete-AND-force CONJUNCTION. pg2-fkmg4's ruling removed the
// conjunction rather than extending it: an explicit force is unsafe on its own, so
// `del && force` is strictly narrower than `force` and the delete half became dead.
// Pinning `'d'` and `"delete"` here would therefore require code the ruling deleted.
//
// It asserts, inside that one function: all THREE fused letters are matched with
// cmdparse.HasShortFlag (a clustering primitive, not an exact token), the short force
// letter too, and the long force spelling with cmdparse.HasLongFlagPrefix — since
// `--force` is not reachable by any short-flag matching, and its `--no-force` negation
// is excluded only because that matcher never matches a token longer than its canonical.
func TestGit_BranchUnsafe_UsesTheRequiredMechanisms(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "git.go", nil, 0)
	if err != nil {
		t.Fatalf("parse git.go: %v", err)
	}

	var fd *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "isBranchUnsafe" {
			fd = d
			break
		}
	}
	if fd == nil {
		t.Fatal("isBranchUnsafe() is absent from git.go — the `git branch` safety gate was deleted; every unguarded spelling (-D/-M/-C/-f) would fall through to modifyingSubcommands[\"branch\"] and APPROVE (pg2-fkmg4, operator ruling pg2-4yy4r item 5)")
	}

	shortBytes := map[string]bool{}      // e.g. "'D'"
	longPrefixNames := map[string]bool{} // e.g. "force"
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok {
			return true
		}
		switch selectorName(call.Fun) {
		case "cmdparse.HasShortFlag":
			if lit.Kind == token.CHAR {
				shortBytes[lit.Value] = true
			}
		case "cmdparse.HasLongFlagPrefix":
			if lit.Kind == token.STRING {
				longPrefixNames[strings.Trim(lit.Value, "`\"")] = true
			}
		}
		return true
	})

	fused := map[string]string{
		"'D'": "-D is --delete FUSED with --force",
		"'M'": "-M is --move FUSED with --force (measured clobbering: keepme bdfdb1f -> bad17ef)",
		"'C'": "-C is --copy FUSED with --force (measured overwriting an existing branch)",
	}
	for want, why := range fused {
		if !shortBytes[want] {
			t.Errorf("isBranchUnsafe does not test short flag %s with cmdparse.HasShortFlag — %s, and it must also CLUSTER (`-Dv`, `-vM`, `-rC`), which no exact-token test can see (pg2-fkmg4)", want, why)
		}
	}
	if !shortBytes["'f'"] {
		t.Error("isBranchUnsafe does not test short flag 'f' with cmdparse.HasShortFlag — an explicit force removes git's guard from delete, move, copy AND creation, so it is unsafe on its own (pg2-fkmg4)")
	}
	if !longPrefixNames["force"] {
		t.Error("isBranchUnsafe does not test long flag \"force\" with cmdparse.HasLongFlagPrefix — `--force` is unreachable by short-flag matching, and the open PREFIX matcher is what covers `--forc`/`--for`/`--f` while excluding the real `--no-force` negation (pg2-fkmg4)")
	}
	// The guarded halves MUST NOT be gated on their own: `-d`, `-m` and `-c` are the
	// spellings git itself refuses in the destructive case, and each has an explicit
	// acceptance criterion keeping it approved.
	for _, forbidden := range []string{"'d'", "'m'", "'c'"} {
		if shortBytes[forbidden] {
			t.Errorf("isBranchUnsafe tests short flag %s — the LOWERCASE forms are GUARDED by git itself and MUST stay approved; gating one contradicts pg2-fkmg4's classification", forbidden)
		}
	}
	// A negation must never be read as its positive form, so the predicate must not
	// hand-roll long-flag matching where `--no-force` could slip through.
	for _, forbidden := range []string{"strings.ToLower", "strings.ToUpper", "strings.EqualFold", "strings.HasPrefix", "cmdparse.HasLongFlag"} {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && selectorName(call.Fun) == forbidden {
				t.Errorf("%s: isBranchUnsafe calls %s — the flag tests MUST be case-SENSITIVE (`-d` != `-D`, `-m` != `-M`, `-c` != `-C`), abbreviation-aware, and must exclude `--no-` negations; use cmdparse.HasShortFlag / cmdparse.HasLongFlagPrefix (pg2-fkmg4)", fset.Position(call.Pos()), forbidden)
			}
			return true
		})
	}
}
