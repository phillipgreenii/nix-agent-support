package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE NARROW `-c` RELAXATION (pg2-arfw6, operator spec S-1..S-8 of 2026-07-28).
//
// hasGitConfigInjection used to abstain on ANY pre-subcommand `-c`, so
// `git -c core.fsmonitor=false diff` prompted while the bare `git diff` approved. It
// now clears a `-c` by lookup in a CLOSED allowlist of (key -> value predicate) PAIRS.
//
// WHY THE PAIRS MATTER, AND WHY THAT IS WHAT THESE TESTS ARE MOSTLY ABOUT.
// `core.fsmonitor` is NOT an inert key: git executes the value as the fsmonitor hook
// unless it is a boolean literal (measured on git 2.54.0 — see hasGitConfigInjection).
// A key-only allowlist would therefore have re-opened the RCE the guard exists for, so
// the value assertions below are the security half of this bead, not a detail of it.
//
// THE RCE REGRESSION GUARDS ARE NOT HERE. They are TestGit_ConfigInjection_Abstain
// (git_test.go) and TestGit_Chdir_ConfigInjection_StillAbstains (git_chdir_test.go),
// which this bead must leave UNMODIFIED — that is an explicit acceptance criterion, and
// the reason these new tests live in their own file.
//
// THE ALLOWLIST HAS SINCE GROWN TWO MORE ENTRIES, UNDER THEIR OWN RULING.
// `core.editor` and `sequence.editor` were added by pg2-6qh3p (operator ruling on
// pg2-agprs, 2026-08-13) with a much narrower predicate; their behaviour is asserted in
// editorcarveout_test.go. Everything in THIS file is about pg2-arfw6's one
// `core.fsmonitor` entry and about the machinery both share — the closedness, the
// all-or-nothing rule, `--config-env`, and relaxation-only.

// dashCBare is the `-c <key>` spelling with NO `=`, which git reads as boolean true.
func dashCBare(key, gitArgs string) string {
	return "git -c " + key + " " + gitArgs
}

// readOnlySubcommandsUnderTest are the read-only invocations pg2-arfw6's acceptance
// criteria name — the shapes the in-corpus traffic actually used.
var readOnlySubcommandsUnderTest = []string{"status", "diff", "log", "show", "merge-base"}

// TestGit_ConfigFlagAllowlist_ClearsBooleanFsmonitor is the bead's headline fixture:
// the ONE allowlisted pair, on the read-only subcommands the corpus used.
//
// It asserts the RELATION to the bare command rather than a literal Approve, because
// "a cleared `-c` changes nothing" IS the S-7 relaxation-only requirement, and it
// survives any later retune of what a read-only git command is worth. The Approve
// floor is asserted separately because the relation alone would be satisfied if both
// spellings regressed together.
func TestGit_ConfigFlagAllowlist_ClearsBooleanFsmonitor(t *testing.T) {
	for _, sub := range readOnlySubcommandsUnderTest {
		bare := evalCmd(t, "git "+sub)
		if bare.Decision != hookio.Approve {
			t.Fatalf("`git %s`: got %s (%s), want approve — the premise of this bead is that the BARE form already approves", sub, bare.Decision, bare.Reason)
		}
		for _, cmd := range []string{
			dashC("core.fsmonitor", "false", sub),
			dashC("core.fsmonitor", "true", sub),
			// Case normalization (S-3): git config keys are case-INSENSITIVE, and
			// `git -c CORE.FSMONITOR=<script> status` was MEASURED executing the
			// script, so a case variant must resolve to the same allowlist entry.
			dashC("core.FSMonitor", "false", sub),
			dashC("CORE.FSMONITOR", "false", sub),
			// The value predicate is case-insensitive too, for the same reason.
			dashC("core.fsmonitor", "FALSE", sub),
			// Every git boolean literal, not just true/false (S-2).
			dashC("core.fsmonitor", "0", sub),
			dashC("core.fsmonitor", "1", sub),
			dashC("core.fsmonitor", "no", sub),
			dashC("core.fsmonitor", "yes", sub),
			dashC("core.fsmonitor", "off", sub),
			dashC("core.fsmonitor", "on", sub),
			// A bare key with no `=` is boolean true to git (S-4, verified: exit 0,
			// nothing executed, no program nameable).
			dashCBare("core.fsmonitor", sub),
			dashCBare("core.FSMonitor", sub),
			// Repeated, all cleared (the passing half of S-6's all-or-nothing).
			"git -c core.fsmonitor=false -c core.fsmonitor=true " + sub,
			// Interleaved with the other pre-subcommand options the walk consumes.
			"git -C /repo -c core.fsmonitor=false " + sub,
			"git -c core.fsmonitor=false -C /repo " + sub,
		} {
			got := evalCmd(t, cmd)
			if got.Decision != bare.Decision {
				t.Errorf("cmd %q: got %s (%s), but the bare `git %s` got %s (%s) — an allowlisted `-c` pair is CLEARED, so it must leave the verdict exactly as it was (pg2-arfw6 S-7)",
					cmd, got.Decision, got.Reason, sub, bare.Decision, bare.Reason)
			}
			if got.Decision != hookio.Approve {
				t.Errorf("cmd %q: got %s (%s), want approve — `git -c core.fsmonitor=false diff|status|…` prompting while the bare form approves IS the defect this bead closes", cmd, got.Decision, got.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagAllowlist_GluedQuoteParity pins the pg2-9zgso fix directly on this
// bead's OWN allowlisted pair. `-c` always takes its `<key>=<value>` pair as ONE
// SEPARATE token (configFlagPairCleared's own doc), so `git -c core.fsmonitor='false'
// status` arrives as the single token `core.fsmonitor='false'` — an UNQUOTED key glued
// to a QUOTED value, which cmdparse's lowering leaves quoted (it strips quoting only
// when the WHOLE token is wrapped, and here only the value half is). That is
// IDENTICAL to the shell's `core.fsmonitor=false`, and must reach the SAME verdict as
// both the plain unquoted spelling and the WHOLE-token-quoted spelling
// (`'core.fsmonitor=false'`, which cmdparse already stripped correctly before this
// bead — the acceptance-criteria pair pg2-9zgso names explicitly).
func TestGit_ConfigFlagAllowlist_GluedQuoteParity(t *testing.T) {
	for _, sub := range readOnlySubcommandsUnderTest {
		unquoted := evalCmd(t, dashC("core.fsmonitor", "false", sub))
		if unquoted.Decision != hookio.Approve {
			t.Fatalf("dashC(core.fsmonitor, false, %q): got %s (%s), want approve — the premise this parity test is built on", sub, unquoted.Decision, unquoted.Reason)
		}
		for _, cmd := range []string{
			dashC("core.fsmonitor", `'false'`, sub), // glued single quotes: KEY='value', one token
			dashC("core.fsmonitor", `"false"`, sub), // glued double quotes
			"git -c 'core.fsmonitor=false' " + sub,  // WHOLE token quoted (already worked pre-fix)
			dashC("core.fsmonitor", `'true'`, sub),  // the OTHER cleared boolean literal, glued
		} {
			got := evalCmd(t, cmd)
			if got.Decision != unquoted.Decision {
				t.Errorf("cmd %q: got %s (%s), want %s (matching the unquoted spelling `-c core.fsmonitor=false`) — both spellings are identical to the shell (pg2-9zgso)", cmd, got.Decision, got.Reason, unquoted.Decision)
			}
			if got.Decision != hookio.Approve {
				t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagAllowlist_NonBooleanValueAbstains is the security half: for the
// allowlisted KEY, a value that is not a boolean literal is a PATHNAME GIT EXECUTES,
// and it must still abstain. These rows are what a key-only allowlist would have
// cleared.
func TestGit_ConfigFlagAllowlist_NonBooleanValueAbstains(t *testing.T) {
	values := []string{
		"/tmp/evil.sh",       // the measured exec case
		"",                   // empty: git-boolean-false, deliberately NOT a member (S-2)
		"touch /tmp/pwned",   // a command with arguments
		"truex",              // a near-miss on a member
		"true2",              // ditto
		"2",                  // a number that is not a git boolean
		"~/evil.sh",          // a path that needs expansion
		"$EVIL",              // not statically visible
		"$(command -v evil)", // a substitution
	}
	for _, value := range values {
		for _, sub := range readOnlySubcommandsUnderTest {
			cmd := dashC("core.fsmonitor", value, sub)
			if got := evalCmd(t, cmd); got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain — `core.fsmonitor` holds the PATHNAME of the fsmonitor hook whenever the value is not a boolean literal, and `git -c core.fsmonitor=<script> status` was MEASURED executing it (pg2-arfw6 S-2)", cmd, got.Decision, got.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagAllowlist_IsClosed pins S-1: membership, not a denylist, is the
// control. Every key here is a real execution sink or config indirection from
// pg2-arfw6's S-8 list, given a value that WOULD satisfy the `core.fsmonitor`
// predicate — so a bug that applied the value predicate without checking the key, or
// that grew the key set, fails here rather than in production.
//
// The last rows are not sinks at all. They are there because the control is closedness:
// an ORDINARY key is not cleared either, and a future key nobody has heard of is
// excluded by the same mechanism.
func TestGit_ConfigFlagAllowlist_IsClosed(t *testing.T) {
	keys := []string{
		// Programs git executes, or reaches by indirection (S-8).
		//
		// `core.editor` and `sequence.editor` are NOT here, and their absence is the
		// point: pg2-6qh3p's own operator ruling added them as carve-out entries with a
		// much narrower predicate. Their closedness is asserted in
		// TestGit_EditorCarveOut_NearMissesDoNotClear, which checks the VALUE boundary
		// they actually have. Everything below is still cleared by nothing at all.
		"core.pager", "core.hooksPath", "core.sshCommand",
		"core.askPass", "core.gitProxy", "gpg.program",
		"diff.external", "diff.mydriver.command", "diff.mydriver.textconv",
		"merge.mydriver.driver", "mergetool.mine.cmd", "difftool.mine.cmd",
		"filter.mine.clean", "filter.mine.smudge", "filter.mine.process",
		"credential.helper", "uploadpack.packObjectsHook",
		"remote.origin.uploadpack", "remote.origin.receivepack",
		"init.templateDir", "pager.log", "web.browser", "help.browser",
		"man.mine.cmd", "alias.x", "include.path", "includeIf.gitdir:/x/.path",
		// Interlocks and redirects.
		"http.sslVerify", "clean.requireForce", "receive.denyCurrentBranch",
		"url.https://evil/.insteadOf", "remote.origin.url",
		// NOT sinks — the point being that closedness does not depend on danger.
		"user.name", "branch.main.remote", "core.autocrlf",
		"future.key.nobodyhasheardof",
		// A SUBSECTIONED spelling of the allowlisted key. It is a DIFFERENT key to git
		// (`core` has no subsections), so collapsing it onto the entry would clear a
		// token the allowlist never named.
		"core.sneaky.fsmonitor",
		// A near-miss on the key itself.
		"core.fsmonitorx", "corefsmonitor", "core.fsmonitor.extra",
	}
	for _, key := range keys {
		for _, cmd := range []string{
			dashC(key, "false", "log"),
			dashC(key, "true", "status"),
			dashCBare(key, "log"),
		} {
			if got := evalCmd(t, cmd); got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain — the `-c` allowlist is CLOSED, so a key that is not a member is not cleared whatever its value (pg2-arfw6 S-1)", cmd, got.Decision, got.Reason)
			}
		}
	}
}

// TestGit_ConfigFlagAllowlist_AllOrNothing pins S-6: one non-cleared `-c` abstains the
// WHOLE command, in either order and with any number of cleared pairs around it. And
// the verdict is Abstain, never Reject — that is the level this guard has always had.
func TestGit_ConfigFlagAllowlist_AllOrNothing(t *testing.T) {
	cmds := []string{
		"git -c core.fsmonitor=false -c core.pager=EVIL log",
		"git -c core.pager=EVIL -c core.fsmonitor=false log",
		"git -c core.fsmonitor=false -c core.fsmonitor=/tmp/evil.sh log",
		"git -c core.fsmonitor=false -c core.fsmonitor=false -c core.editor=EVIL log",
		"git -c core.fsmonitor=false -C /repo -c core.pager=EVIL log",
		"git -c core.fsmonitor=false --config-env=core.pager=X log",
		// A trailing `-c` has no pair to inspect: fail closed.
		"git -c",
		"git -c core.fsmonitor=false -c",
	}
	for _, cmd := range cmds {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain — EVERY `-c` in the pre-subcommand span must clear, and one that does not abstains the whole command (pg2-arfw6 S-6)", cmd, got.Decision, got.Reason)
		}
		if got.Decision == hookio.Reject {
			t.Errorf("cmd %q: got REJECT — a non-cleared `-c` stays Abstain; raising it to a decisive refusal needs its own ruling (pg2-arfw6 S-6)", cmd)
		}
	}
}

// TestGit_ConfigFlagAllowlist_ConfigEnvStaysUnconditional pins S-5. `--config-env`
// takes its value from the ENVIRONMENT, so there is nothing in the command text for a
// value predicate to evaluate — including when the key is the allowlisted one, which is
// the row a naive shared code path would clear.
func TestGit_ConfigFlagAllowlist_ConfigEnvStaysUnconditional(t *testing.T) {
	cmds := []string{
		"git --config-env=core.fsmonitor=SOMEVAR log",
		"git --config-env core.fsmonitor=SOMEVAR log",
		"git --config-env=core.fsmonitor=SOMEVAR status",
		"git --config-env=core.pager=SOMEVAR log",
		"git --config-env core.pager=SOMEVAR log",
		"git -C /repo --config-env=core.fsmonitor=SOMEVAR log",
	}
	for _, cmd := range cmds {
		if got := evalCmd(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain — `--config-env` names an ENVIRONMENT VARIABLE, so no value predicate can be evaluated against the command text (pg2-arfw6 S-5)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_ConfigFlagAllowlist_RelaxationOnly pins S-7 as a RELATION over the whole
// verdict range: a cleared `-c` yields exactly the verdict the bare command has, so
// nothing is upgraded. The decisive rows are the ones that matter — a cleared `-c` must
// not become the cheap way past `git tag`, a force-push, or `git reset --hard`.
//
// It deliberately states no literal verdicts. Several of these arms have been retuned
// already (pg2-ur9zc, pg2-u0e0c, pg2-fkmg4) and a hardcoded pair would go stale at the
// next ruling, while the relation is what this bead actually promises.
func TestGit_ConfigFlagAllowlist_RelaxationOnly(t *testing.T) {
	subs := []string{
		"tag v1",
		"push --force origin main",
		"push -f origin main",
		"push origin main",
		"remote add upstream https://example.invalid/x.git",
		"config core.hooksPath /tmp/h",
		"config remote.origin.url https://evil.invalid/x.git",
		"config clean.requireForce false",
		"clean -fdx",
		"reset --hard HEAD~1",
		"reset --soft HEAD~1",
		"branch -D feat",
		"rebase -i HEAD~1",
		"bisect start",
		"commit -m msg",
		"add .",
		"checkout -b feat",
		"worktree add ../wt",
	}
	for _, sub := range subs {
		bare := evalCmd(t, "git "+sub)
		for _, cmd := range []string{
			dashC("core.fsmonitor", "false", sub),
			dashCBare("core.fsmonitor", sub),
		} {
			got := evalCmd(t, cmd)
			if got.Decision != bare.Decision {
				t.Errorf("cmd %q: got %s (%s), but the bare `git %s` got %s (%s) — clearing a `-c` only removes the injection early-return; it MUST NOT change the verdict classify would have reached (pg2-arfw6 S-7)",
					cmd, got.Decision, got.Reason, sub, bare.Decision, bare.Reason)
			}
		}
	}
	// The one shape the acceptance criteria name literally, because "unchanged" and
	// "not Approve" are two different claims and a regression could satisfy the first.
	if got := evalCmd(t, "git -c core.fsmonitor=false push --force"); got.Decision == hookio.Approve {
		t.Errorf("`git -c core.fsmonitor=false push --force`: got APPROVE (%s) — the relaxation must never upgrade a mutating or destructive subcommand", got.Reason)
	}
}

// TestGit_ConfigFlagAllowlist_ChdirDemotionStillApplies pins the second half of S-7:
// the `-C` chdirSafe demotion runs AFTER the injection guard, so clearing a `-c` must
// not let a read-only command into an unsafe directory unremarked.
//
// It compares against the same command WITHOUT the `-c`, so it asserts the relation the
// bead promises rather than re-deriving the path-zone policy, which lives in
// git_chdir_test.go and is not this bead's to restate.
func TestGit_ConfigFlagAllowlist_ChdirDemotionStillApplies(t *testing.T) {
	r := newWithProject(t)
	for _, sub := range []string{"status", "log", "add ."} {
		bare := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc "+sub, projectCWD)))
		withFlag := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc -c core.fsmonitor=false "+sub, projectCWD)))
		if withFlag.Decision != bare.Decision {
			t.Errorf("`git -C /etc -c core.fsmonitor=false %s`: got %s (%s), but without the `-c` it got %s (%s) — the `-C` demotion applies after the injection guard and a cleared `-c` must not bypass it (pg2-arfw6 S-7)",
				sub, withFlag.Decision, withFlag.Reason, bare.Decision, bare.Reason)
		}
		if withFlag.Decision == hookio.Approve {
			t.Errorf("`git -C /etc -c core.fsmonitor=false %s`: got APPROVE (%s) — /etc is outside every safe zone", sub, withFlag.Reason)
		}
	}
}

// TestGit_ConfigFlagAllowlist_TableShape is the mechanical guard on the allowlist
// itself. The behavioural tests above only know the keys someone remembered to list, so
// they cannot notice a NEW entry being added; this can.
//
// It also pins the two invariants the lookup depends on: keys are stored LOWERCASED
// (configFlagPairCleared lowercases before matching, so a mixed-case entry would be
// permanently unreachable and would read as a live allowance that never fires), and
// every entry carries a real predicate rather than a nil that would panic.
//
// AND IT PINS WHICH PREDICATE EACH KEY GETS, because the two are NOT interchangeable:
// `isGitBooleanLiteral` on an editor key would clear `-c core.editor=1`, and
// `isInertEditorValue` on `core.fsmonitor` would start prompting the boolean traffic
// pg2-arfw6 relieves. A "simplification" that shares one predicate fails here.
func TestGit_ConfigFlagAllowlist_TableShape(t *testing.T) {
	// The complete expected membership, with the ruling that authorized each entry.
	// Adding a row here is the deliberate act; adding one to the table alone fails.
	want := map[string]string{
		"core.fsmonitor": "pg2-arfw6, operator spec S-2 of 2026-07-28 — value must be a git boolean literal",
		// The editor carve-out entries are pg2-6qh3p's, under the operator ruling on
		// pg2-agprs of 2026-08-13. They are NOT a widening of the `core.fsmonitor`
		// entry: separate ruling, separate and much narrower predicate. They exist so
		// the ARGV spellings get the same carve-out as GIT_EDITOR /
		// GIT_SEQUENCE_EDITOR, which is constraint (a) of that ruling.
		"core.editor":     "pg2-6qh3p, operator ruling of 2026-08-13 — value must be one of two EXACT inert literals",
		"sequence.editor": "pg2-6qh3p, operator ruling of 2026-08-13 — value must be one of two EXACT inert literals",
	}
	for key, why := range want {
		if _, ok := clearedConfigFlagPairs[key]; !ok {
			t.Errorf("clearedConfigFlagPairs is missing %q (%s) — was the relaxation deleted? `git -c core.fsmonitor=false diff` would abstain again while the bare `git diff` approves, and a missing editor key would break the env-never-less-restrictive-than-argv relation (pg2-6c85x)", key, why)
		}
	}
	// The pairing itself. A predicate is a func value, so it is compared by asking it
	// about a value only ONE of the two accepts.
	if pred := clearedConfigFlagPairs["core.fsmonitor"]; pred != nil && !pred("off") {
		t.Error(`clearedConfigFlagPairs["core.fsmonitor"] rejects "off" — it must be the git-boolean-literal predicate, not the editor one; swapping them would start prompting exactly the traffic pg2-arfw6 exists to relieve`)
	}
	for _, key := range []string{"core.editor", "sequence.editor"} {
		if pred := clearedConfigFlagPairs[key]; pred != nil && pred("1") {
			t.Errorf("clearedConfigFlagPairs[%q] accepts \"1\" — it must be the EXACT-token inert-editor predicate, not the git-boolean one; `-c %s=1` would name a program called `1` (pg2-6qh3p constraint (b))", key, key)
		}
	}
	for key, predicate := range clearedConfigFlagPairs {
		if _, expected := want[key]; !expected {
			t.Errorf("clearedConfigFlagPairs has an UNEXPECTED entry %q — the allowlist is the security control (pg2-arfw6 S-1), so every member needs an operator ruling and a row in this test's `want` map naming it", key)
		}
		if key != lowerASCII(key) {
			t.Errorf("clearedConfigFlagPairs key %q is not lowercased — configFlagPairCleared lowercases the key before matching, so this entry can never be reached and reads as an allowance that silently never fires", key)
		}
		if predicate == nil {
			t.Errorf("clearedConfigFlagPairs[%q] has a nil value predicate — a key-only allowance is exactly what pg2-arfw6 rejected, and this would panic on lookup", key)
		}
	}
	// The predicate itself, at its boundary: the classes that make the pair sound.
	for _, value := range []string{"true", "false", "1", "0", "yes", "no", "on", "off", "TRUE", "Off", "YeS"} {
		if !isGitBooleanLiteral(value) {
			t.Errorf("isGitBooleanLiteral(%q) = false — git parses config booleans case-insensitively from exactly this set", value)
		}
	}
	for _, value := range []string{"", " ", "true ", " true", "truex", "2", "-1", "/tmp/evil.sh", "t", "f", "y", "n"} {
		if isGitBooleanLiteral(value) {
			t.Errorf("isGitBooleanLiteral(%q) = true — anything that is not an explicit boolean literal is a PATHNAME git executes for `core.fsmonitor`, and the empty value is deliberately excluded (pg2-arfw6 S-2)", value)
		}
	}
}

// lowerASCII is the test's own lowercaser, kept separate from strings.ToLower so the
// invariant it checks cannot be satisfied by the same call the production lookup makes
// being wrong in the same way.
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
