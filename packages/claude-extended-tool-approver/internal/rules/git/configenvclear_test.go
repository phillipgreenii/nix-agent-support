package git

import (
	"fmt"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE ENV TWIN OF clearedConfigFlagPairs (pg2-py8h2).
//
// pg2-a12rl made hasGitConfigEnvInjection unconditionally screen every
// `GIT_CONFIG_COUNT`/`KEY_n`/`VALUE_n` triple, and pg2-arfw6 separately taught the `-c`
// route to CLEAR an already-allowlisted pair (clearedConfigFlagPairs). That left one
// idiom — `export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor
// GIT_CONFIG_VALUE_0=false` — prompting on exactly the fsmonitor-hygiene pattern this
// workspace's own tooling uses, while its `-c` twin was already relieved. This file
// pins that the two spellings now reach the SAME verdict for a pair that clears, and
// that every fail-closed shape the acceptance criteria name still screens.
//
// clearedConfigEnvPairValues names, for each allowlisted key, ONE value its own
// predicate accepts — reused across every test below so the table stays in one place.
var clearedConfigEnvPairValues = map[string]string{
	"core.fsmonitor":  "false",
	"core.editor":     "true",
	"sequence.editor": ":",
}

// TestGit_ConfigEnvTriple_MatchesTheDashCRoute_ForAClearedPair is the acceptance
// criterion stated as a RELATION: for a pair already in clearedConfigFlagPairs, the
// env triple spelling reaches the SAME verdict as the equivalent `-c` flag, across
// every subcommand whose base verdict is Approve — and that verdict is Approve, not
// merely "the same NoOpinion floor as before".
func TestGit_ConfigEnvTriple_MatchesTheDashCRoute_ForAClearedPair(t *testing.T) {
	for key, value := range clearedConfigEnvPairValues {
		for _, sub := range approveClassSubcommands {
			envCmd := envTriple(key, value, sub)
			argvCmd := dashC(key, value, sub)
			envGot := evalCmd(t, envCmd)
			argvGot := evalCmd(t, argvCmd)
			if envGot.Decision != argvGot.Decision {
				t.Errorf("key %q value %q, `git %s`: env spelling got %s (%s) but the -c spelling got %s (%s) — a cleared pair MUST reach the same verdict on both spellings (pg2-py8h2)",
					key, value, sub, envGot.Decision, envGot.Reason, argvGot.Decision, argvGot.Reason)
			}
			if envGot.Decision != hookio.Approve {
				t.Errorf("cmd %q: got %s (%s), want APPROVE — %q=%q is in clearedConfigFlagPairs, so the env triple must clear exactly as the -c spelling does", envCmd, envGot.Decision, envGot.Reason, key, value)
			}
		}
	}
}

// TestGit_ConfigEnvTriple_CrossLeafExportClears is the bead's headline scenario,
// spelled the way the corpus actually writes it: a PERSISTENT `export` ahead of the
// git call, which pg2-xjt1s's visibleEnvVars already threads through to this screen.
// It is the exact idiom named in the bead title ("pg2-xjt1s makes 'export
// GIT_CONFIG_COUNT=1 KEY_0=core.fsmonitor VALUE_0=false' prompt") and must now clear.
func TestGit_ConfigEnvTriple_CrossLeafExportClears(t *testing.T) {
	for key, value := range clearedConfigEnvPairValues {
		prefix := fmt.Sprintf("GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=%s GIT_CONFIG_VALUE_0=%s", key, value)
		for _, sep := range exportSeparators {
			gitLeaf := "git status"
			expr := fmt.Sprintf("export %s%s%s", prefix, sep, gitLeaf)
			got := evalLeaf(t, expr, gitLeaf)
			if got.Decision != hookio.Approve {
				t.Errorf("expr %q: got %s (%s), want APPROVE — the persistent export of an already-allowlisted pair must clear exactly as its inline/`-c` twins do (pg2-py8h2)", expr, got.Decision, got.Reason)
			}
		}
	}
}

// TestGit_ConfigEnvTriple_MultiPairAllCleared pins the ALL-CLEAR shape of the
// all-or-nothing rule: a COUNT > 1 whose EVERY pair clears must clear as a whole, not
// just a single-pair triple.
func TestGit_ConfigEnvTriple_MultiPairAllCleared(t *testing.T) {
	cmd := "GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=core.editor GIT_CONFIG_VALUE_1=true git status"
	if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want APPROVE — every pair in the triple is individually allowlisted, so the whole triple must clear", cmd, got.Decision, got.Reason)
	}
}

// TestGit_ConfigEnvTriple_FailsClosed enumerates the acceptance criteria's named
// fail-closed shapes, each built around a pair that WOULD clear if it resolved or were
// allowlisted — the cases configenv_test.go's own FailsClosed suite (pg2-a12rl,
// intentionally left unmodified) never exercises, because every one of its rows uses a
// key/value that was never going to clear in the first place.
func TestGit_ConfigEnvTriple_FailsClosed(t *testing.T) {
	cmds := []string{
		// An unresolvable VALUE_n on an otherwise-allowlisted key.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=$V git status",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=$(true) git status",
		// An unresolvable KEY_n paired with an otherwise-allowlisted value.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=$K GIT_CONFIG_VALUE_0=false git status",
		// A COUNT that disagrees with the keys present, around an otherwise-clearable
		// pair: COUNT says 2 but only ONE pair is supplied.
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false git status",
		// A COUNT that disagrees the other way: COUNT says 1 but TWO pairs are
		// present (an extra pair outside the claimed range).
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=core.editor GIT_CONFIG_VALUE_1=true git status",
		// A KEY_n naming a key NOT in the allowlist, with a value that would satisfy
		// core.fsmonitor's own predicate were the key different.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=false git status",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.external GIT_CONFIG_VALUE_0=true git status",
		// An allowlisted key whose value fails THAT key's own predicate.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=maybe git status",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.editor GIT_CONFIG_VALUE_0=vim git status",
		// MIXED SETS: one pair clears, the other does not — none may clear.
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=core.pager GIT_CONFIG_VALUE_1=false git status",
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=core.editor GIT_CONFIG_VALUE_1=vim git status",
		// A KEY_n with no matching VALUE_n, otherwise allowlisted.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor git status",
		// A VALUE_n with no matching KEY_n.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_VALUE_0=false git status",
		// The pair is not at index 0, otherwise allowlisted and otherwise clearable.
		"GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_1=core.fsmonitor GIT_CONFIG_VALUE_1=false git status",
		// Case variance on the KEY is fine to git (measured elsewhere in this
		// package) but the VALUE predicate for core.editor/sequence.editor is an
		// EXACT-TOKEN match — "TRUE" is not "true" and must not clear.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.editor GIT_CONFIG_VALUE_0=TRUE git status",
	}
	for _, cmd := range cmds {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — this triple must fail closed and remain screened (pg2-py8h2)", cmd, got.Reason)
		}
	}
}

// TestGit_ConfigEnvTriple_CrossLeafFailsClosed is TestGit_ConfigEnvTriple_FailsClosed's
// cross-leaf twin: the SAME unresolvable/mixed shapes, reached through a persistent
// `export` ahead of the git call rather than an inline prefix, so the fail-closed
// property survives pg2-xjt1s's cross-leaf threading too.
func TestGit_ConfigEnvTriple_CrossLeafFailsClosed(t *testing.T) {
	rows := []string{
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=$V; git status",
		"export GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false; git status",
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=false; git status",
		"export GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false GIT_CONFIG_KEY_1=core.pager GIT_CONFIG_VALUE_1=false; git status",
	}
	for _, expr := range rows {
		got := evalLeaf(t, expr, "git status")
		if got.Decision == hookio.Approve {
			t.Errorf("expr %q: got APPROVE (%s) — the cross-leaf export spelling of a shape that must fail closed inline must also fail closed here (pg2-py8h2)", expr, got.Reason)
		}
	}
}

// TestGit_ConfigEnvTriple_DoesNotWidenGatedKeys re-runs
// TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened's real-table iteration (pg2-a12rl,
// unmodified) but with a VALUE from clearedConfigEnvPairValues rather than "/tmp/evil",
// to pin that the new carve-out cannot accidentally clear a key gatedConfigKeys/
// gatedConfigSections names — the carve-out is scoped to clearedConfigFlagPairs' own
// three keys ONLY, never to "any key with an inert-looking value".
func TestGit_ConfigEnvTriple_DoesNotWidenGatedKeys(t *testing.T) {
	for id := range gatedConfigKeys {
		if _, allowlisted := clearedConfigFlagPairs[id]; allowlisted {
			// core.editor / core.fsmonitor / sequence.editor are deliberately members
			// of BOTH tables (each with its own operator ruling) — that overlap is
			// the point of the carve-out and is covered by the MatchesTheDashCRoute
			// test above, not this boundary check.
			continue
		}
		for _, sub := range []string{"status", "log", "commit -m msg"} {
			cmd := envTriple(id, "false", sub)
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — %q is a gatedConfigKeys entry outside clearedConfigFlagPairs, so a boolean-looking value must not let it clear (pg2-py8h2)", cmd, got.Reason, id)
			}
		}
	}
}
