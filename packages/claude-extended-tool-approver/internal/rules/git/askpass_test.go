package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// `core.askPass` — THE PERSISTENT SPELLING WAS THE UNSCREENED ONE (pg2-h1ori).
//
// pg2-6c85x screened `GIT_ASKPASS` on MEASURED evidence (git 2.54.0, 2026-08-13:
// `git credential fill` ran a marker script as the "Username for …" prompt —
// scripts/probe-pg2-6c85x.sh), and the `-c` route screens `core.askPass` because that
// route is key-BLIND. But the key was absent from `gatedConfigKeys`, so the one spelling
// that OUTLIVES the command — the porcelain write, which applies to every later git
// operation in the repo — was the approved one. Measured on main @ a064a73e, 2026-08-14
// (scripts/probe-pg2-h1ori.sh):
//
//	GIT_ASKPASS=/tmp/evil git fetch origin        -> {}
//	git -c core.askPass=/tmp/evil fetch origin    -> {}
//	git config core.askPass /tmp/evil             -> allow      <- the persistent one
//	git config --global core.askPass /tmp/evil    -> allow
//	git config --unset core.askPass               -> allow
//
// EVERY ASSERTION HERE IS A RELATION, for the reason configenv_test.go states: the three
// spellings do NOT share one verdict and must not be pinned to three literals. The
// porcelain gate is a decisive Ask while both injection routes are the weaker refusal, and
// that ORDERING is deliberate — gatedConfigKeys' "WHY THE INTERLOCK AND SINK CLASSES ARE
// ASK" argues the porcelain gate must sit AT OR ABOVE the injection control it mirrors.
// Literals here would go stale at the next retune of either route and would hide a drift
// in the ordering, which is the property that actually matters.

// TestGit_AskPass_EveryConfigSinkWriteAgrees is the self-maintaining relation, and the one
// that pins `core.askPass` WITHOUT naming it: every configSink key in the real table
// reaches the SAME `git config` write verdict, because configGateResult derives the verdict
// from the CLASS rather than storing it per key.
//
// This is the assertion that was FAILING before pg2-h1ori — `core.askPass` was not in the
// table, so its write reached the ordinary-write Approve while every one of its peers
// asked. A key added to the table extends this test with no edit here, and a key REMOVED
// from it fails here rather than silently going quiet.
func TestGit_AskPass_EveryConfigSinkWriteAgrees(t *testing.T) {
	var sinks []string
	for id, class := range gatedConfigKeys {
		if class == configSink {
			sinks = append(sinks, id)
		}
	}
	if len(sinks) < 2 {
		t.Fatalf("gatedConfigKeys holds %d configSink keys — too few for the agreement relation to mean anything", len(sinks))
	}
	// The reference is whichever sink `git config` answers for first; the claim is
	// AGREEMENT, so any member serves and the test names no verdict of its own.
	refKey := sinks[0]
	ref := evalCmd(t, "git config "+refKey+" /tmp/evil")
	if ref.Decision == hookio.Approve {
		t.Fatalf("`git config %s /tmp/evil`: got APPROVE (%s) — the whole configSink gate is gone, not just one key", refKey, ref.Reason)
	}
	for _, id := range sinks {
		cmd := "git config " + id + " /tmp/evil"
		got := evalCmd(t, cmd)
		if got.Decision != ref.Decision {
			t.Errorf("cmd %q: got %s (%s), but the sibling configSink `git config %s /tmp/evil` got %s — configGateResult derives the verdict from the CLASS, so two keys of one class MUST NOT answer differently; `core.askPass` was the key this caught (pg2-h1ori)",
				cmd, got.Decision, got.Reason, refKey, ref.Decision)
		}
	}
}

// TestGit_AskPass_ThreeSpellingsAreConsistent is the acceptance criterion stated as the
// relation it actually is: no spelling of this sink reaches Approve, and the PERSISTENT
// spelling is never the weakest of the three.
//
// The second clause is the one that names the defect. A porcelain write that survives the
// command cannot be more permissive than an inline injection that dies with it — that
// inversion is exactly what made the asymmetry worth a bead rather than a footnote.
func TestGit_AskPass_ThreeSpellingsAreConsistent(t *testing.T) {
	// Every write spelling the key can be reached through, including the ones that shift
	// the key out of first operand position (the pg2-szadj defect class).
	writes := []string{
		"git config core.askPass /tmp/evil",
		"git config --global core.askPass /tmp/evil",
		"git config --local core.askPass /tmp/evil",
		"git config --add core.askPass /tmp/evil",
		"git config --unset core.askPass",
		"git config set core.askPass /tmp/evil",
		"git config -f .git/config core.askPass /tmp/evil",
		"git config --type=path core.askPass /tmp/evil",
		// Section and variable names are case-INsensitive to git (measured 2.54.0), and
		// configKeyID lowercases for exactly this reason.
		"git config CORE.AskPass /tmp/evil",
		"git config core.ASKPASS /tmp/evil",
		"git -C /tmp/repo config core.askPass /tmp/evil",
	}
	for _, cmd := range writes {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — git EXECUTES this value to obtain a credential (marker evidence in scripts/probe-pg2-6c85x.sh), and this is the spelling that OUTLIVES the command", cmd, got.Reason)
		}
	}
	// The ordering relation, across the three routes, on the subcommands the injection
	// routes actually reach.
	for _, sub := range approveClassSubcommands {
		porcelain := evalCmd(t, "git config core.askPass /tmp/evil")
		env := evalCmd(t, envProg("GIT_ASKPASS", "/tmp/evil", sub))
		argv := evalCmd(t, dashC("core.askPass", "/tmp/evil", sub))
		for label, got := range map[string]hookio.RuleResult{"GIT_ASKPASS env": env, "-c core.askPass argv": argv} {
			if got.Decision == hookio.Approve {
				t.Errorf("%s, `git %s`: got APPROVE (%s) — one sink, three spellings, and none of them may approve", label, sub, got.Reason)
			}
			if porcelain.Decision < got.Decision {
				t.Errorf("`git config core.askPass /tmp/evil` got %s (%s), which is LESS restrictive than the %s spelling's %s on `git %s` — the PERSISTENT write must never be the cheapest route to a sink that dies with the command (pg2-h1ori)",
					porcelain.Decision, porcelain.Reason, label, got.Decision, sub)
			}
		}
	}
}

// TestGit_AskPass_ReadsAndNeighboursAreUntouched pins the boundary of the addition. A
// gated key gates WRITES only, and the key next door is not this key — without these rows
// the change could have been a blanket `core.*` gate and every test above would still pass.
func TestGit_AskPass_ReadsAndNeighboursAreUntouched(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// Reads are not gated — configIsRead short-circuits before the key scan.
		{"git config --get core.askPass", hookio.Approve, "a read is not a write"},
		{"git config --get-all core.askPass", hookio.Approve, "same"},
		{"git config core.askPass", hookio.Approve, "the one-operand form is a read"},
		{"git config --list", hookio.Approve, "no key at all"},
		// Neighbouring keys that merely LOOK like it.
		{"git config core.askPassword /tmp/evil", hookio.Approve, "a longer variable name is a different key"},
		{"git config askpass.core /tmp/evil", hookio.Approve, "section and name reversed is not the key"},
		{"git config user.askPass /tmp/evil", hookio.Approve, "another section's variable of the same name is not this key"},
		// TEXT IS NOT AN OPERATION — the pg2-5b901 class, which is what keeps this bead's
		// own bookkeeping runnable.
		{`git commit -m "gate core.askPass in gatedConfigKeys (pg2-h1ori)"`, hookio.Approve, "a mention in a commit message is text"},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", row.cmd, got.Decision, got.Reason, row.want, row.why)
		}
	}
}
