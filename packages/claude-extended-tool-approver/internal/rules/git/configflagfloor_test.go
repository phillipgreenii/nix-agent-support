package git

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE `-c` SCREEN IS A FLOOR, NOT A REPLACEMENT (pg2-6f4q9).
//
// hasGitConfigInjection's answer used to be consumed as a PRE-CLASSIFY SHORT-CIRCUIT:
// Evaluate returned before the subcommand was classified, so the refusal replaced EVERY
// verdict — the decisive ones included. Measured through the real binary on main
// @ a064a73e, 2026-08-14 (scripts/probe-pg2-6f4q9.sh), each of these is `deny` WITHOUT
// the `-c`:
//
//	git -c user.name=x push --force origin main                   -> {}
//	git -c user.name=x tag v1                                     -> {}
//	git -c x=y remote add upstream https://example.invalid/x.git   -> {}
//	git -c x=y config remote.origin.url https://evil.invalid/x.git -> {}
//
// So prefixing ANY pre-subcommand `-c <k>=<v>` laundered a hard Reject into an
// auto-approvable non-decision, and `user.name` — a key with no bearing on any hazard in
// this file — is the extreme case. pg2-arfw6 repaired only the sliver where the pair
// CLEARS (a cleared `-c` reaches classify by construction); every UNCLEARED pair, which
// is every key outside clearedConfigFlagPairs, still laundered.
//
// EVERY ASSERTION HERE IS A RELATION AGAINST THE SAME COMMAND WITHOUT THE `-c`, and that
// is the acceptance criterion rather than a stylistic choice. The subcommand arms this
// compares against have been retuned three times already (pg2-ur9zc moved `reset --hard`,
// pg2-u0e0c moved `clean`, pg2-fkmg4 moved `branch`), so a hardcoded verdict pair would
// go stale at the next ruling while the relation is what the floor actually promises.
// configflag_test.go's TestGit_ConfigFlagAllowlist_RelaxationOnly is the same relation for
// the CLEARED half; this file is the uncleared half, and between them the `-c` route is
// pinned across its whole range.
//
// THE TWO DIRECTIONS ARE ASSERTED SEPARATELY BECAUSE THEY CAN FAIL SEPARATELY. Reverting
// the floor to a short-circuit fails only DecisiveVerdictsSurvive; "simplifying" it to a
// bare Approve-only demotion (dropping the not-applicable arm) fails only
// NotApplicableStaysScreened; deleting the screen fails ApproveIsStillWithdrawn.

// unclearedConfigFlagKeys are pre-subcommand `-c` keys that clearedConfigFlagPairs does
// NOT clear, so each still reaches the injection screen. The set spans the whole reason a
// key might be there: two of the bead's own measured shapes (`user.name`, `x.y` — keys
// with no hazard at all, which is what makes the laundering absurd), one configSink from
// gatedConfigKeys, and a cleared key given an UNCLEARED value.
var unclearedConfigFlagKeys = []string{
	"user.name=x",
	"x=y",
	"core.pager=EVIL",
	"core.hooksPath=/tmp/h",
	"core.fsmonitor=/tmp/evil.sh", // an allowlisted KEY whose VALUE does not clear
}

// decisiveSubcommands are invocations whose base verdict is decisive (Ask or Reject) —
// the ones the short-circuit erased. The list is not asserted to BE decisive: the tests
// read the bare verdict and act on what they find, so a later ruling that moves one of
// these off Ask/Reject moves the test with it.
var decisiveSubcommands = []string{
	"tag v1",
	"push --force origin main",
	"push -f origin main",
	"remote add upstream https://example.invalid/x.git",
	"remote set-url origin https://evil.invalid/x.git",
	"config remote.origin.url https://evil.invalid/x.git",
	"config url.https://evil.invalid/.insteadOf https://github.com/",
	"config core.hooksPath /tmp/h",
	"config clean.requireForce false",
}

// notClassifiedSubcommands are invocations classify answers NOT-APPLICABLE for. They are
// the arm a bare Approve-only demotion would have left unscreened, and `clean --help` is
// the one that would have gone to `allow`: safecmds approves `git <sub> --help` as a
// man-page read (see the `clean` arm in classify), and git spawns the caller's pager to
// page it.
var notClassifiedSubcommands = []string{
	"bisect start",
	"notes list",
	"clean --help",
	"stripspace",
	"maintenance run",
}

// TestGit_ConfigFlagFloor_NeverLessRestrictiveThanWithoutTheFlag is the whole-range
// relation, and the one assertion that would still hold if every other test here were
// deleted: adding a pre-subcommand `-c` may make a command MORE restrictive, never less.
//
// It names no verdict at all, so it survives any retune of any arm it crosses.
func TestGit_ConfigFlagFloor_NeverLessRestrictiveThanWithoutTheFlag(t *testing.T) {
	subs := append(append([]string{}, decisiveSubcommands...), notClassifiedSubcommands...)
	subs = append(subs, approveClassSubcommands...)
	subs = append(subs, "clean -fdx", "reset --hard HEAD~1", "branch -D feat", "rebase -i HEAD~1")
	for _, key := range unclearedConfigFlagKeys {
		for _, sub := range subs {
			bare := evalCmd(t, "git "+sub)
			got := evalCmd(t, "git -c "+key+" "+sub)
			if got.Decision < bare.Decision {
				t.Errorf("`git -c %s %s`: got %s (%s), which is LESS restrictive than the bare `git %s`'s %s (%s) — an injected config pair must never be the cheaper way around a verdict (pg2-6f4q9)",
					key, sub, got.Decision, got.Reason, sub, bare.Decision, bare.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagFloor_DecisiveVerdictsSurvive is the bead's headline fixture: where
// the bare verdict is DECISIVE, the `-c` spelling reaches the SAME verdict.
//
// Equality is the right assertion only here. On an Approve-class subcommand the screen
// deliberately withdraws the approval, so the two spellings differ by design — that is
// ApproveIsStillWithdrawn below. The decisive/non-decisive split is read from the bare
// command rather than declared, which is what keeps this test correct across a retune.
func TestGit_ConfigFlagFloor_DecisiveVerdictsSurvive(t *testing.T) {
	for _, key := range unclearedConfigFlagKeys {
		for _, sub := range decisiveSubcommands {
			bare := evalCmd(t, "git "+sub)
			if bare.Decision != hookio.Ask && bare.Decision != hookio.Reject {
				t.Logf("`git %s` is no longer decisive (%s) — this row now only carries the relation test", sub, bare.Decision)
				continue
			}
			got := evalCmd(t, "git -c "+key+" "+sub)
			if got.Decision != bare.Decision {
				t.Errorf("`git -c %s %s`: got %s (%s), but the bare `git %s` got %s (%s) — the injection screen is a FLOOR and MUST NOT replace a decisive verdict; that shape let `git -c user.name=x push --force` measure `{}` while the bare force-push was `deny` (pg2-6f4q9)",
					key, sub, got.Decision, got.Reason, sub, bare.Decision, bare.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagFloor_ApproveIsStillWithdrawn is the other half of fail-closed: the
// screen must still do the job pg2-b3eow wrote it for. An uncleared `-c` on an
// otherwise-approvable command MUST NOT reach Approve — `-c core.pager=EVIL log` is the
// RCE class, and making the screen a floor must not soften it.
func TestGit_ConfigFlagFloor_ApproveIsStillWithdrawn(t *testing.T) {
	for _, key := range unclearedConfigFlagKeys {
		for _, sub := range approveClassSubcommands {
			cmd := "git -c " + key + " " + sub
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — an uncleared pre-subcommand `-c` hands git configuration of the caller's choosing and MUST NOT reach Approve, whatever the subcommand", cmd, got.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagFloor_NotApplicableStaysScreened pins the arm that makes the floor a
// floor rather than an Approve-only demotion.
//
// classify answers NOT-APPLICABLE for these subcommands, so it contributes NO verdict for
// a demotion to withdraw. Under a bare Approve-only demotion the `-c` would go entirely
// unscreened here, and `git -c core.pager=EVIL clean --help` would reach the safecmds
// man-page Approve with the caller's pager named. The rule must refuse instead — which
// also floors the leaf, so the later safecmds Approve cannot lift it (hookio.MostRestrictive).
func TestGit_ConfigFlagFloor_NotApplicableStaysScreened(t *testing.T) {
	for _, key := range unclearedConfigFlagKeys {
		for _, sub := range notClassifiedSubcommands {
			cmd := "git -c " + key + " " + sub
			got := evalCmd(t, cmd)
			if got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — classify does not own this subcommand, so the `-c` screen is the ONLY thing standing between the caller's config and an approval", cmd, got.Reason)
			}
			if out := string(hookio.FormatOutput(got, nil)); out != "{}" {
				t.Errorf("cmd %q: emitted %s, want {} — this arm must keep the verdict it had before pg2-6f4q9, which was measured `{}` on main @ a064a73e", cmd, out)
			}
		}
	}
	// A bare `git -c k=v` with NO subcommand at all: there is no verdict for the floor to
	// sit under, so the refusal stands rather than the leaf becoming one no rule examined.
	for _, cmd := range []string{"git -c x=y", "git -c core.pager=EVIL", "git --config-env=core.pager=X"} {
		got := evalCmd(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — a `-c` with no subcommand must not be approved", cmd, got.Reason)
		}
		if out := string(hookio.FormatOutput(got, nil)); out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {} (unchanged from before pg2-6f4q9)", cmd, out)
		}
	}
}

// TestGit_ConfigFlagFloor_EmitsExpectedHookOutput is the BOUNDARY assertion, and it is not
// redundant with the Decision-level tests: what Claude Code RECEIVES is the serialized
// output, and only `deny`/`ask` there actually stop a command.
//
// The two directions are checked in one place on purpose — the withdrawn Approve must
// serialize to `{}` and specifically must not carry `permissionDecision: "allow"`, while
// the restored Reject must serialize to a `deny` that names the real reason rather than
// silently vanishing into `{}`.
func TestGit_ConfigFlagFloor_EmitsExpectedHookOutput(t *testing.T) {
	for _, cmd := range []string{
		"git -c core.pager=EVIL log",
		"git -c user.name=x status",
		"git -c x=y bisect start",
	} {
		out := string(hookio.FormatOutput(evalCmd(t, cmd), nil))
		if out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {}", cmd, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("cmd %q: emitted %s, which contains an allow decision", cmd, out)
		}
	}
	for _, cmd := range []string{
		"git -c user.name=x tag v1",
		"git -c user.name=x push --force origin main",
		"git -c x=y remote add upstream https://example.invalid/x.git",
		"git -c x=y config remote.origin.url https://evil.invalid/x.git",
	} {
		out := string(hookio.FormatOutput(evalCmd(t, cmd), nil))
		if !strings.Contains(out, `"deny"`) {
			t.Errorf("cmd %q: emitted %s, want a deny — this is one of the four shapes pg2-6f4q9 measured emitting `{}` while the same command without the `-c` was `deny`", cmd, out)
		}
	}
}

// TestGit_ConfigFlagFloor_ChdirDemotionStillComposes pins that the floor did not disturb
// the ORDER of the demotions that follow it. A `-C` into an unsafe zone and an uncleared
// `-c` must compose to a non-approval, and the reason must come from one of them rather
// than from a verdict neither owns.
func TestGit_ConfigFlagFloor_ChdirDemotionStillComposes(t *testing.T) {
	r := newWithProject(t)
	for _, sub := range []string{"status", "log", "add ."} {
		cmd := "git -C /etc -c core.pager=EVIL " + sub
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — /etc is outside every safe zone AND the `-c` is uncleared", cmd, got.Reason)
		}
	}
}
