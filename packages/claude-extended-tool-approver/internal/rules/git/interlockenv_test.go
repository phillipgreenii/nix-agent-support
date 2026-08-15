package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE ENV SPELLING THAT REMOVES A REFUSAL GIT MAKES BY DEFAULT (pg2-nd6i3).
//
// This is the THIRD env family in this rule, and the sibling of programenv_test.go's.
// pg2-a12rl closed the config-SOURCE route (`GIT_CONFIG*`); pg2-6c85x and pg2-qi1jo
// closed the program-NAMING route (`GIT_EXTERNAL_DIFF`, `GIT_SSH_COMMAND`,
// `GIT_PROXY_COMMAND`, …). The INTERLOCK route was left open by both, for a table-shape
// reason recorded at the time: these variables name no program, so the program-naming
// table — whose own check requires every twin to resolve as a configSink — had no seat
// for them, and this file had no third table.
//
// MEASURED ON GIT 2.54.0 WITH A MARKER, which is what separates this from a plausible
// guess. `ext::` is git's arbitrary-command transport: with the protocol unlocked the
// "URL" IS a command line git executes.
//
//	GIT_ALLOW_PROTOCOL=ext git ls-remote 'ext::<marker> %S'                 -> marker RAN as `git-upload-pack`
//	GIT_PROTOCOL_FROM_USER=1 -c protocol.ext.allow=user … 'ext::<marker>'   -> marker RAN as `git-upload-pack`
//	git ls-remote 'ext::<marker> %S'                    (no variable)       -> `fatal: transport 'ext' not allowed`
//	GIT_PROTOCOL_FROM_USER=0 -c protocol.ext.allow=user … 'ext::<marker>'   -> `fatal: transport 'ext' not allowed`
//
// And the verdicts that made it a hole, on main @004f370c at the hook-output boundary:
// all three env spellings measured `approve` while the `-c` twins measured `abstain` and
// the `git config` twins measured `ask` — so the env route was the LOOSEST of the three
// for both gated interlock keys.
//
// GIT_PROTOCOL_FROM_USER APPEARS IN NO EARLIER BEAD. It came out of the enumeration
// pg2-nd6i3 required (203 `GIT_*` names in the git binary's own string table, 14 of them
// interlock-shaped), and it is the reason that enumeration was a criterion rather than a
// courtesy: a screen covering the two variables already named and missing this one would
// have been the same family split the alternate-transport instruction exists to prevent.
//
// EVERY ASSERTION IS A RELATION over gitInterlockEnvVars rather than a literal verdict
// pair, for the reason programenv_test.go gives for its own: a hardcoded pair goes stale
// the moment either route is retuned, and both have been retuned before. The literal
// spellings are asserted in exactly one place —
// TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened's family block — because a
// variable DELETED from the table is invisible to every table-iterating test.

// interlockDashC renders the `-c <key>=<loosening>` spelling of an interlock twin. The
// value is the one that actually REMOVES the refusal for each key, because a relation
// against a value that changes nothing would compare the env route to a no-op and pass
// for the wrong reason.
func interlockDashC(twin, gitArgs string) string {
	loosening := "always"
	if twin == "http.sslVerify" {
		loosening = "false"
	}
	return "git -c " + twin + "=" + loosening + " " + gitArgs
}

// interlockEnvValue is the value that removes the refusal, per variable. Membership in
// the screen is value-BLIND (see gitInterlockEnvVars), so these are the REALISTIC
// loosening spellings rather than the only ones screened.
func interlockEnvValue(name string) string {
	switch name {
	case "GIT_ALLOW_PROTOCOL":
		return "ext"
	default:
		return "1"
	}
}

// TestGit_InterlockEnvVar_TwinIsAConfigInterlockInTheRealTable derives the screen's
// justification FROM gatedConfigKeys instead of restating it, exactly as its
// program-family counterpart does. Each screened variable declares the config key it is
// the env spelling of, and that key must resolve in this package's own table as
// configInterlock — so the screen cannot claim a twin the table does not class as
// "removes a refusal git makes by default".
//
// A twin that resolved as configSink would belong in gitProgramEnvVars instead, and one
// absent from gatedConfigKeys entirely would mean the screen rests on a key nobody gated
// — the failure this check exists to make loud rather than silent.
func TestGit_InterlockEnvVar_TwinIsAConfigInterlockInTheRealTable(t *testing.T) {
	if len(gitInterlockEnvVars) == 0 {
		t.Fatal("gitInterlockEnvVars is empty — the interlock env screen was deleted")
	}
	for name, twin := range gitInterlockEnvVars {
		_, id, ok := configKeyID(twin)
		if !ok {
			t.Errorf("%s: twin %q is not key-shaped, so configKeyID cannot resolve it against gatedConfigKeys", name, twin)
			continue
		}
		class, gated := gatedConfigKeys[id]
		if !gated {
			t.Errorf("%s: twin %q (id %q) is NOT in gatedConfigKeys — either that entry was removed (then this variable's justification is gone) or a new interlock twin needs its own ruling and its own replay", name, twin, id)
			continue
		}
		if class != configInterlock {
			t.Errorf("%s: twin %q (id %q) is class %d, not configInterlock — a program-NAMING twin belongs in gitProgramEnvVars, whose own check enforces configSink; mixing the classes is what made this screen necessary in the first place", name, twin, id, class)
		}
	}
	// NO VARIABLE MAY SIT IN BOTH TABLES. The two screens return different reasons and
	// rest on different measurements, so a variable in both would make which reason a
	// reader sees depend on evaluation order.
	for name := range gitInterlockEnvVars {
		if _, alsoProgram := gitProgramEnvVars[name]; alsoProgram {
			t.Errorf("%s: appears in BOTH gitInterlockEnvVars and gitProgramEnvVars", name)
		}
	}
}

// TestGit_InterlockEnvVar_IsNeverLooserThanTheConfigSpelling is the acceptance criterion
// stated as a relation: for each interlock key, the ENV spelling is never LESS
// restrictive than the `-c` spelling of the same loosening.
//
// That is the shape pg2-6c85x established for the program-naming family, and it is what
// keeps this closed rather than merely fixed: a future variable added to the table
// inherits the assertion, and a future retune of either route is caught the first time
// the two disagree.
func TestGit_InterlockEnvVar_IsNeverLooserThanTheConfigSpelling(t *testing.T) {
	subs := append([]string{
		"ls-remote origin",
		"fetch origin",
		"clone https://example.invalid/x.git",
		"pull origin main",
		"push origin main",
		"submodule update --init",
	}, approveClassSubcommands...)
	for name, twin := range gitInterlockEnvVars {
		for _, sub := range subs {
			envCmd := interlockEnvValue(name)
			envGot := evalCmd(t, name+"="+envCmd+" git "+sub)
			argvGot := evalCmd(t, interlockDashC(twin, sub))
			if envGot.Decision < argvGot.Decision {
				t.Errorf("%s, `git %s`: env spelling got %s (%s), which is LESS restrictive than the -c %s spelling's %s (%s) — the env route must never be the cheaper way around the same interlock",
					name, sub, envGot.Decision, envGot.Reason, twin, argvGot.Decision, argvGot.Reason)
			}
		}
	}
}

// TestGit_InterlockEnvVar_NeverReachesApprove is the floor, and it is not implied by the
// relation above: two routes can AGREE on Approve and satisfy the relation while leaving
// the hole wide open. That is exactly the state main @004f370c was in for the `git
// config` route before pg2-qi1jo, so the floor is asserted separately.
func TestGit_InterlockEnvVar_NeverReachesApprove(t *testing.T) {
	for name := range gitInterlockEnvVars {
		for _, sub := range approveClassSubcommands {
			cmd := name + "=" + interlockEnvValue(name) + " git " + sub
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("`%s`: got APPROVE (%s) — this variable removes a refusal git makes by default, and an approve auto-runs the invocation it protects", cmd, got.Reason)
			}
		}
	}
}

// TestGit_InterlockEnvVar_IsValueBlind pins the decision recorded in
// gitInterlockEnvVars' doc, in BOTH directions, because value-blindness is a choice with
// a cost and the cost should be visible in a test rather than only in a comment.
//
// The loosening spellings are screened, AND so are the strictly MORE RESTRICTIVE ones
// (`GIT_SSL_NO_VERIFY=0`, an empty `GIT_ALLOW_PROTOCOL=` which allows NOTHING,
// `GIT_PROTOCOL_FROM_USER=0`). That second half is a knowingly accepted false positive:
// reading the value would mean re-deriving git's own boolean and colon-list parsing for
// three variables whose entire corpus presence is probe scripts and bead bodies. If a
// later change starts reading values, this test is where that decision has to be
// revisited — it is not asserting that value-blindness is RIGHT, only that it is what
// the code does.
func TestGit_InterlockEnvVar_IsValueBlind(t *testing.T) {
	for _, tc := range []struct{ cmd, why string }{
		{"GIT_SSL_NO_VERIFY=0 git fetch origin", "0 leaves verification ON"},
		{"GIT_SSL_NO_VERIFY= git fetch origin", "an empty value leaves verification ON"},
		{"GIT_ALLOW_PROTOCOL= git ls-remote origin", "an empty allowlist permits NOTHING"},
		{"GIT_PROTOCOL_FROM_USER=0 git ls-remote origin", "0 marks the protocol as NOT user-supplied, which is more restrictive"},
	} {
		if got := evalCmd(t, tc.cmd); got.Decision == hookio.Approve {
			t.Errorf("`%s`: got APPROVE — the screen is documented as VALUE-BLIND, so this spelling is expected to be screened too (%s). If value-reading was introduced deliberately, update gitInterlockEnvVars' doc and this test together", tc.cmd, tc.why)
		}
	}
}

// TestGit_InterlockEnvVar_DoesNotOverreach is the false-positive direction, and its rows
// are the negative controls for the enumeration rather than an arbitrary sample. Each is
// interlock-SHAPED — it appears in the same 14-name slice of the git binary's string
// table that produced the three screened variables — and each is deliberately absent
// from the screen, for a reason gitInterlockEnvVars' doc records per variable.
//
// If a later change widens the screen to a `GIT_` prefix or to a name pattern, this is
// the test that fails.
func TestGit_InterlockEnvVar_DoesNotOverreach(t *testing.T) {
	for _, tc := range []struct{ cmd, why string }{
		{"GIT_TERMINAL_PROMPT=0 git fetch origin", "suppresses prompting: strictly MORE restrictive"},
		{"GIT_NO_REPLACE_OBJECTS=1 git log", "DISABLES replace refs, which are the hazard"},
		{"GIT_NO_LAZY_FETCH=1 git log", "partial-clone behaviour, not a refusal"},
		{"GIT_FORCE_THREADS=1 git status", "performance, not a refusal"},
		{"GIT_SSH_VARIANT=ssh git fetch origin", "selects a dialect, names no program and removes no refusal"},
		{"GIT_ALLOW_PROTOCOLX=ext git ls-remote origin", "a longer name is a different variable"},
		{"git_allow_protocol=ext git ls-remote origin", "lowercase is not git's variable"},
	} {
		if got := evalCmd(t, tc.cmd); got.Decision != hookio.Approve {
			t.Errorf("`%s`: got %s (%s), want APPROVE — %s, so screening it is a pure false positive and this screen must stay keyed on an exact, enumerated name set", tc.cmd, got.Decision, got.Reason, tc.why)
		}
	}
}

// TestGit_InterlockEnvVar_DoesNotWeakenADecisiveVerdict pins the DEMOTION shape, which is
// the property a re-implementation is most likely to lose.
//
// The screen withdraws an APPROVE; it is not a pre-classify short-circuit. That shape was
// measured turning `git tag` and a force-push from `deny` into an auto-approvable `{}`,
// which is why pg2-6c85x declined to copy it and why pg2-6f4q9 took it off the `-c` route.
// So a decisive verdict must survive the env prefix unchanged.
func TestGit_InterlockEnvVar_DoesNotWeakenADecisiveVerdict(t *testing.T) {
	for _, sub := range []string{
		"push --force origin main",
		"clean -fdx",
		"reset --hard HEAD~1",
		"branch -D feat",
		"config --global protocol.ext.allow always",
	} {
		bare := evalCmd(t, "git "+sub)
		for name := range gitInterlockEnvVars {
			withEnv := evalCmd(t, name+"="+interlockEnvValue(name)+" git "+sub)
			if withEnv.Decision < bare.Decision {
				t.Errorf("%s, `git %s`: bare got %s (%s) but with the env prefix got %s (%s) — the screen must DEMOTE an Approve, never weaken a decisive verdict",
					name, sub, bare.Decision, bare.Reason, withEnv.Decision, withEnv.Reason)
			}
		}
	}
}
