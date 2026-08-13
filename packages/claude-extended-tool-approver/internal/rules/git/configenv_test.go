package git

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE ENV SPELLING OF GIT CONFIG INJECTION (pg2-a12rl).
//
// `gatedConfigKeys` classes `core.fsmonitor`, `core.pager`, `diff.external` and their
// siblings as configSink — git EXECUTES the value during an ordinary operation — and
// hasGitConfigInjection defers a pre-subcommand `-c` as a known RCE class. The ENV
// route to the same sinks was unscreened: measured through the real binary in this
// worktree, 2026-08-13, `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor
// GIT_CONFIG_VALUE_0=/tmp/evil git status` emitted `permissionDecision: "allow"` while
// `git -c core.fsmonitor=/tmp/evil status` emitted `{}`.
//
// The tests here are written as RELATIONS between the two spellings wherever a
// relation is what the rule promises, because a hardcoded verdict pair goes stale the
// moment either route is retuned — and both routes have been retuned before (pg2-szadj,
// pg2-ur9zc, pg2-u0e0c). See hasGitConfigEnvInjection for the measured
// variable-by-variable evidence and the design ruling.

// envTriple is the `GIT_CONFIG_COUNT`/`KEY_0`/`VALUE_0` spelling of one key=value
// pair, as a leading assignment prefix on a git command.
func envTriple(key, value, gitArgs string) string {
	return fmt.Sprintf("GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=%s GIT_CONFIG_VALUE_0=%s git %s", key, value, gitArgs)
}

// dashC is the argv spelling of the same pair.
func dashC(key, value, gitArgs string) string {
	return fmt.Sprintf("git -c %s=%s %s", key, value, gitArgs)
}

// approveClassSubcommands are the git invocations whose BASE verdict (before any
// injection screening) is Approve. The relation the two routes must satisfy exactly
// only holds here — see TestGit_ConfigEnvInjection_IsNeverLessRestrictiveThanDashC for
// the weaker relation that holds everywhere, and why the difference is deliberate.
var approveClassSubcommands = []string{
	"status",
	"log --oneline -5",
	"diff",
	"describe --dirty",
	"commit -m msg",
	"add .",
	"checkout -b feat",
	"rebase main",
	"fetch origin",
	"push origin main",
	"reset --soft HEAD~1",
	"config --get user.email",
	"config x y",
	"remote -v",
}

// TestGit_ConfigEnvInjection_MatchesTheDashCRoute is the fixture the acceptance
// criteria require: for the SAME key, the env spelling and the `-c` spelling reach the
// SAME verdict.
//
// IT ASSERTS THE RELATION, NOT A LITERAL. Nothing here names Approve, NoOpinion, Ask or
// Reject for the injection itself, so retuning the `-c` route (a level, a reason, or the
// decision to make it decisive) cannot leave the env route behind: the assertion fails
// until BOTH move. That is the property the bead asks for — the two spellings of one
// hazard must not drift apart again, which is exactly how this hole was created.
//
// The one literal it does assert is a FLOOR: neither spelling may be Approve. That is
// not a duplicate of the equality — two routes could agree on Approve and satisfy
// equality while re-opening the hole.
func TestGit_ConfigEnvInjection_MatchesTheDashCRoute(t *testing.T) {
	// One key per gate class, plus a key in NO gate table at all. The un-gated key is
	// the interesting row: `-c` is key-BLIND, so it screens `user.name` too, and the
	// env route must be key-blind for the same reason (a `GIT_CONFIG_GLOBAL` file's
	// keys are not knowable from argv at all). A future key-aware rewrite of either
	// route breaks this row first.
	keys := []string{
		"core.fsmonitor", // configSink — the measured hole
		"core.pager",     // configSink
		"diff.external",  // configSink
		"merge.mergiraf.driver",
		"clean.requireForce", // configInterlock
		"remote.origin.url",  // configRedirect
		"pager.log",          // a gated SECTION
		"CORE.FSMonitor",     // config keys are case-INSENSITIVE to git (measured)
		"user.name",          // gated by NO table
		"branch.main.remote", // gated by NO table
	}
	for _, key := range keys {
		for _, sub := range approveClassSubcommands {
			envCmd := envTriple(key, "/tmp/evil", sub)
			argvCmd := dashC(key, "/tmp/evil", sub)
			envGot := evalCmd(t, envCmd)
			argvGot := evalCmd(t, argvCmd)
			if envGot.Decision != argvGot.Decision {
				t.Errorf("key %q, `git %s`: env spelling got %s (%s) but the -c spelling got %s (%s) — one hazard, two spellings, and they MUST reach the same verdict (pg2-a12rl); if a route was deliberately retuned, retune BOTH",
					key, sub, envGot.Decision, envGot.Reason, argvGot.Decision, argvGot.Reason)
			}
			if envGot.Decision == hookio.Approve {
				t.Errorf("key %q, `git %s`: env spelling got APPROVE — git honours the GIT_CONFIG_* triple as command-line-equivalent config, and the marker probe (scripts/probe-pg2-a12rl.sh) shows it reaching the execution sink for real", key, sub)
			}
		}
	}
}

// TestGit_ConfigEnvInjection_IsNeverLessRestrictiveThanDashC is the relation that holds
// for EVERY git invocation, including the ones whose base verdict is decisive.
//
// WHY THE STRICT EQUALITY ABOVE IS SCOPED TO THE Approve CLASS. The two routes are wired
// into Evaluate differently, deliberately: hasGitConfigInjection answers BEFORE classify,
// so it REPLACES every verdict, while hasGitConfigEnvInjection only withdraws an Approve.
// Measured through the real binary, this worktree, 2026-08-13: `git -c user.name=x tag v1`
// and `git -c user.name=x push --force origin main` each emitted `{}`, though both are
// `deny` without the `-c` — so the incumbent route lets an irrelevant config pair WEAKEN a
// hard Reject into an auto-approvable non-decision. The env route MUST NOT copy that, so
// on those subcommands it is strictly MORE restrictive than the `-c` route, and equality
// would be the wrong assertion.
//
// This test states the invariant that survives either fixing that defect or leaving it:
// the env route is never the weaker of the two. It uses no literal verdict, so it holds
// whichever way a later bead rules.
func TestGit_ConfigEnvInjection_IsNeverLessRestrictiveThanDashC(t *testing.T) {
	subs := append([]string{
		"tag v1",
		"push --force origin main",
		"push -f origin main",
		"remote add upstream https://example.invalid/x.git",
		"config core.hooksPath /tmp/h",
		"config remote.origin.url https://evil.invalid/x.git",
		"clean -fdx",
		"reset --hard HEAD~1",
		"branch -D feat",
		"bisect start",
	}, approveClassSubcommands...)
	for _, sub := range subs {
		envGot := evalCmd(t, envTriple("core.fsmonitor", "/tmp/evil", sub))
		argvGot := evalCmd(t, dashC("core.fsmonitor", "/tmp/evil", sub))
		if envGot.Decision < argvGot.Decision {
			t.Errorf("`git %s`: env spelling got %s (%s), which is LESS restrictive than the -c spelling's %s (%s) — the env route must never be the cheaper way around the same guard (pg2-a12rl)",
				sub, envGot.Decision, envGot.Reason, argvGot.Decision, argvGot.Reason)
		}
	}
}

// TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened iterates the REAL gatedConfigKeys
// and gatedConfigSections tables rather than a copy of them, which is what makes the
// coverage self-maintaining: a key added to those tables is a key this test starts
// requiring the env route to screen, with no edit here.
//
// It is not redundant with the relation test above, which samples one key per class. A
// key-aware rewrite of the env screen could pass that sample and still miss an entry;
// this cannot.
func TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened(t *testing.T) {
	var keys []string
	for id := range gatedConfigKeys {
		// The table's identity form is `<section>.<name>` with the middle subsection
		// dropped, so an entry like `diff.textconv` is spelled with a driver in real
		// use. Both spellings must be screened, and a key-blind screen makes that
		// automatic — which is the point being pinned.
		keys = append(keys, id)
		if section, _, ok := strings.Cut(id, "."); ok {
			keys = append(keys, section+".mydriver."+id[len(section)+1:])
		}
	}
	for section := range gatedConfigSections {
		keys = append(keys, section+".log")
	}
	if len(keys) == 0 {
		t.Fatal("gatedConfigKeys and gatedConfigSections are both empty — the config gate tables were deleted")
	}
	for _, key := range keys {
		for _, sub := range []string{"status", "log", "commit -m msg"} {
			cmd := envTriple(key, "/tmp/evil", sub)
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — %q is in this package's own gate tables, so the env spelling of a write to it must not auto-approve (pg2-a12rl)", cmd, got.Reason, key)
			}
		}
	}
}

// TestGit_ConfigEnvInjection_FailsClosed is the acceptance criterion that an
// UNPARSEABLE or PARTIALLY-PARSED env prefix must not reach Approve.
//
// Every row is a spelling from which the KEY cannot be read, or cannot be trusted to be
// the whole story: a count with no pairs, a count that outruns its pairs, a dynamic
// value, a shell-quoted key, a config FILE whose contents argv cannot see, git's own
// `-c` propagation channel. A key-aware screen would have to answer each of these
// individually and would fail open on whichever it did not anticipate; the name-keyed
// screen answers all of them by construction, and this test is what proves the
// construction is actually load-bearing.
//
// The `COUNT` rows are also where real git refuses outright — measured 2026-08-13,
// `GIT_CONFIG_COUNT=2` with one pair answers `error: missing config key
// GIT_CONFIG_KEY_1` / `fatal: unable to parse command-line config`. The verdict here
// does NOT rest on that: git's refusal is a property of today's git, and the screen must
// not depend on it.
func TestGit_ConfigEnvInjection_FailsClosed(t *testing.T) {
	cmds := []string{
		// Partial / malformed triples.
		"GIT_CONFIG_COUNT=1 git status",
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status",
		"GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor git status",
		"GIT_CONFIG_COUNT= GIT_CONFIG_KEY_0= GIT_CONFIG_VALUE_0= git status",
		"GIT_CONFIG_COUNT=notanumber GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=x git log",
		// The pair is not at index 0.
		"GIT_CONFIG_COUNT=3 GIT_CONFIG_KEY_2=core.fsmonitor GIT_CONFIG_VALUE_2=/tmp/evil git status",
		"GIT_CONFIG_COUNT=11 GIT_CONFIG_KEY_10=core.fsmonitor GIT_CONFIG_VALUE_10=/tmp/evil git status",
		// The key or value is not statically visible.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=$K GIT_CONFIG_VALUE_0=$V git status",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=$(mktemp) git status",
		`GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0='core.fsmonitor' GIT_CONFIG_VALUE_0="/tmp/evil" git status`,
		// A config FILE: its keys are whatever the file says when git opens it.
		"GIT_CONFIG_GLOBAL=/tmp/evil.cfg git status",
		"GIT_CONFIG_SYSTEM=/tmp/evil.cfg git status",
		"GIT_CONFIG=/tmp/evil.cfg git status",
		"GIT_CONFIG_GLOBAL=/dev/null git status",
		// git's own `-c` propagation channel, packed into one quoted string.
		"GIT_CONFIG_PARAMETERS='core.fsmonitor=/tmp/evil' git status",
		// A GIT_CONFIG_* name this rule has never heard of, including one that only
		// SUPPRESSES config. Over-approximate on purpose: an unknown name is exactly
		// the case a name enumeration would fail open on.
		"GIT_CONFIG_NOSYSTEM=1 git status",
		"GIT_CONFIG_FUTURE_SPELLING=x git status",
		// Position and wrapper forms: the assignment reaches pc.EnvVars either way.
		"env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status",
		"GIT_DIR=/other GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git log",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git -C /repo status",
		// The real in-corpus idiom (2026-08-13 ask log): neutralising a merge driver
		// for one rebase. It is screened, and that IS the measured prompt-volume cost
		// this bead accepted — `merge.<driver>.driver` is a configSink in this
		// package's own table, so no key-aware design would have spared it either.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=merge.mergiraf.driver GIT_CONFIG_VALUE_0= git rebase --autostash origin/main",
	}
	for _, cmd := range cmds {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — an env prefix that carries a GIT_CONFIG* variable MUST NOT reach Approve, whether or not its key is readable (pg2-a12rl)", cmd, got.Reason)
		}
	}
}

// TestGit_ConfigEnvInjection_DoesNotWeakenADecisiveVerdict is the other half of
// fail-closed, and the reason the screen is a DEMOTION rather than the `-c` route's
// pre-classify short-circuit.
//
// A more-restrictive change must not make anything LESS restrictive as a side effect. If
// the env screen answered before classify — the shape hasGitConfigInjection has — then
// prefixing any irrelevant config pair would turn `git tag`, a force-push and the
// redirect-class config write from `deny` into an auto-approvable `{}`. These rows fail
// the moment someone "simplifies" the call site to match the `-c` one.
func TestGit_ConfigEnvInjection_DoesNotWeakenADecisiveVerdict(t *testing.T) {
	rows := []struct {
		sub  string
		want hookio.Decision
		why  string
	}{
		{"tag v1", hookio.Reject, "git tag is prohibited in this workflow"},
		{"push --force origin main", hookio.Reject, "force-push is an operator-ruled Reject"},
		{"push -f origin main", hookio.Reject, "same, short spelling"},
		{"remote add upstream https://example.invalid/x.git", hookio.Reject, "a remote mutation is an exfiltration vector"},
		{"config remote.origin.url https://evil.invalid/x.git", hookio.Reject, "the config spelling of `git remote set-url`"},
		{"config core.hooksPath /tmp/h", hookio.Ask, "a configSink porcelain write asks"},
		{"config clean.requireForce false", hookio.Ask, "a configInterlock porcelain write asks"},
	}
	for _, row := range rows {
		for _, cmd := range []string{
			envTriple("user.name", "x", row.sub),
			envTriple("core.fsmonitor", "/tmp/evil", row.sub),
			"GIT_CONFIG_GLOBAL=/tmp/evil.cfg git " + row.sub,
		} {
			if got := evalCmd(t, cmd); got.Decision != row.want {
				t.Errorf("cmd %q: got %s (%s), want %s — %s; a GIT_CONFIG* prefix must never be the cheaper way around a decisive verdict (pg2-a12rl)", cmd, got.Decision, got.Reason, row.want, row.why)
			}
		}
	}
}

// TestGit_ConfigEnvInjection_DoesNotOverreach pins the boundary of the screen. Each row
// is a command git itself treats as carrying NO caller-supplied configuration, so gating
// it would be a false prompt on ordinary traffic — the cost gatedConfigKeys' own survey
// calls "a large false-positive surface over the routine traffic this rule exists to keep
// flowing".
func TestGit_ConfigEnvInjection_DoesNotOverreach(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// ENV VAR NAMES ARE CASE-SENSITIVE to git's getenv. Measured 2026-08-13: the
		// lowercase triple did NOT run the fsmonitor marker, so git read it as an
		// ordinary variable and so must this rule.
		{"git_config_count=1 git_config_key_0=core.fsmonitor git_config_value_0=/tmp/evil git status", hookio.Approve, "lowercase names are not the variables git reads"},
		{"GIT_CONFIGURATION=1 git status", hookio.Approve, "not a GIT_CONFIG_* variable — no underscore boundary"},
		{"MY_GIT_CONFIG_COUNT=1 git status", hookio.Approve, "a variable whose name merely ENDS with the spelling is not it"},
		// Ordinary traffic, and the unrelated GIT_* variables this rule already models.
		{"git status", hookio.Approve, "no assignment at all"},
		{"FOO=bar git status", hookio.Approve, "an unrelated assignment"},
		{"GIT_DIR=/other git log", hookio.Approve, "the redirect policy for a READ is unchanged"},
		{"GIT_DIR=/other git commit -m msg", hookio.Ask, "the redirect Ask for a WRITE is unchanged"},
		{"GIT_SEQUENCE_EDITOR=: git rebase -i main", hookio.Approve, "the rebase editor carve-out is unchanged"},
		// TEXT IS NOT AN OPERATION — the pg2-5b901 class. A GIT_CONFIG_* spelling
		// quoted in a message is an ARGUMENT, never an assignment, so this bead's own
		// bookkeeping stays runnable.
		{`git commit -m "screen GIT_CONFIG_COUNT/KEY_0/VALUE_0 (pg2-a12rl)"`, hookio.Approve, "a mention in a commit message is text"},
		{`git commit -m "GIT_CONFIG_GLOBAL=/tmp/x measured allow before the fix"`, hookio.Approve, "same, with an = in the text"},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", row.cmd, got.Decision, got.Reason, row.want, row.why)
		}
	}
}

// TestGit_ConfigEnvInjection_EmitsEmptyHookOutput is the BOUNDARY-LEVEL assertion, and
// it is not redundant with the rule-level tests: asserting the internal Decision cannot
// show what Claude Code actually RECEIVES. The withdrawn Approve must serialize to `{}`
// — the same output the `-c` route produces — and specifically must not carry
// `permissionDecision: "allow"`, which is what this bead measured before the fix.
//
// hookio.FormatOutput is the exact function cmd/claude-extended-tool-approver's
// handlePreToolUse writes to stdout. The chain-level twin — proving no LATER rule
// re-approves the leaf — is TestIntegration_GitConfigEnvInjection_EmitsEmptyObject in
// the engine suite.
func TestGit_ConfigEnvInjection_EmitsEmptyHookOutput(t *testing.T) {
	for _, cmd := range []string{
		envTriple("core.fsmonitor", "/tmp/evil", "status"),
		envTriple("core.pager", "/tmp/evil", "log"),
		"GIT_CONFIG_GLOBAL=/tmp/evil.cfg git status",
		"GIT_CONFIG_PARAMETERS='core.fsmonitor=/tmp/evil' git status",
	} {
		got := evalCmd(t, cmd)
		out := string(hookio.FormatOutput(got, nil))
		if out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {} — `permissionDecision: \"allow\"` is what this bead measured before the fix, and it auto-approves a command that runs a program named in its own env prefix", cmd, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("cmd %q: emitted %s, which contains an allow decision", cmd, out)
		}
	}
}
