package git

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE INERT-VALUE EDITOR CARVE-OUT (pg2-6qh3p; operator ruling on pg2-agprs, 2026-08-13,
// also stored as bd memory `ceta-editor-carveout-ruling`).
//
// The exact literal values `true` and `:` are allowed; every other value is screened; and
// it applies to the WHOLE editor family in BOTH spellings — `GIT_EDITOR`,
// `GIT_SEQUENCE_EDITOR`, `git -c core.editor=`, `git -c sequence.editor=`.
//
// WHAT IT BUYS, BOTH HALVES MEASURED. It closes the GIT_SEQUENCE_EDITOR env-route bypass
// of an argv screen (that variable was MEASURED running a marker on
// `.git/rebase-merge/git-rebase-todo` in pg2-6c85x and DECLINED there because this rule's
// own rebase arm requires it), and it removes the friction pg2-6c85x introduced — 65 of
// its 97 newly-prompting rows were `GIT_EDITOR=true git rebase --continue/--skip`.
//
// THE TESTS ARE ORGANISED BY THE RULING'S THREE CONSTRAINTS, because each is a distinct
// way to get this wrong:
//
//	(a) the carve-out reaches the ARGV spellings too   -> EnvMatchesArgv, and pg2-6c85x's
//	                                                      own relation tests, UNMODIFIED
//	(b) the allowance is EXACT-TOKEN, never a pattern  -> NearMissesDoNotClear
//	(c) a non-literal value fails closed               -> NonLiteralValuesFailClosed
//
// EVERY CROSS-SPELLING ASSERTION IS A RELATION rather than a literal verdict, per this
// package's convention (see configenv_test.go): a hardcoded pair goes stale the moment
// either route is retuned, and both have been retuned repeatedly.

// envEditor and argvEditor are the two spellings of "make this the editor", so a test can
// state the relation between them without restating either one's shape.
func envEditor(name, value, gitArgs string) string {
	return name + "=" + value + " git " + gitArgs
}

func argvEditor(key, value, gitArgs string) string {
	return dashC(key, value, gitArgs)
}

// editorSpellingPairs is the (env variable, argv config key) correspondence the ruling
// names. It is derived from the REAL tables rather than restated, so a variable or key
// leaving one of them fails here instead of silently reducing coverage.
func editorSpellingPairs(t *testing.T) map[string]string {
	t.Helper()
	pairs := map[string]string{}
	for name := range gitEditorEnvVars {
		twin, screened := gitProgramEnvVars[name]
		if !screened {
			t.Fatalf("%s is in gitEditorEnvVars but NOT in gitProgramEnvVars — a value carve-out on a variable that is not screened at all is a no-op, and for GIT_SEQUENCE_EDITOR it would silently reopen the measured exec sink pg2-6qh3p closed", name)
		}
		if _, cleared := clearedConfigFlagPairs[twin]; !cleared {
			t.Fatalf("%s's argv twin %q is NOT in clearedConfigFlagPairs — constraint (a) of the ruling: carving out only the env half makes the env spelling LESS restrictive than argv, which is the relation pg2-6c85x established", name, twin)
		}
		pairs[name] = twin
	}
	if len(pairs) != 2 {
		t.Fatalf("expected exactly the two editor variables (GIT_EDITOR, GIT_SEQUENCE_EDITOR), got %d: %v — a wider value-reading set needs its own operator ruling (pg2-6qh3p constraint (b))", len(pairs), pairs)
	}
	return pairs
}

// TestGit_EditorCarveOut_InertValuesDoNotPrompt is the ruling's headline acceptance
// criterion, on the idiom that generated the measured prompt volume.
//
// It asserts the RELATION to the same command with NO editor assignment, which is what
// "inert" means operationally: an inert editor changes nothing. The Approve floor is
// asserted separately, because the relation alone would hold if both spellings regressed.
func TestGit_EditorCarveOut_InertValuesDoNotPrompt(t *testing.T) {
	// The measured idiom, plus the neighbouring editor-taking commands from the same rows.
	//
	// `revert --continue` is deliberately ABSENT: `revert` is in no subcommand set, so it
	// reaches this rule's terminal Abstain with or without an editor. The RELATION holds
	// there (an inert editor changes nothing), but the Approve floor does not, and
	// asserting it would be asserting a verdict this bead has no ruling for.
	subs := []string{"rebase --continue", "rebase --skip", "commit --amend", "commit -m msg"}
	for name, twin := range editorSpellingPairs(t) {
		for _, sub := range subs {
			bare := evalCmd(t, "git "+sub)
			for _, value := range []string{"true", ":"} {
				for _, cmd := range []string{
					envEditor(name, value, sub),
					argvEditor(twin, value, sub),
				} {
					got := evalCmd(t, cmd)
					if got.Decision != bare.Decision {
						t.Errorf("cmd %q: got %s (%s), but the bare `git %s` got %s (%s) — %q is INERT, so the assignment must change nothing (pg2-6qh3p)",
							cmd, got.Decision, got.Reason, sub, bare.Decision, bare.Reason, value)
					}
					if got.Decision != hookio.Approve {
						t.Errorf("cmd %q: got %s (%s), want approve — `GIT_EDITOR=true git rebase --continue/--skip` was 65 of pg2-6c85x's 97 newly-prompting rows, and removing that cost is half of what this ruling buys", cmd, got.Decision, got.Reason)
					}
				}
			}
		}
	}
	// The rebase arm's own carve-out, which is the OTHER half: an interactive rebase needs
	// the sequence editor PRESENT, and an inert one must satisfy that without being
	// screened. This is the shape pg2-a12rl's landed test pins.
	for _, cmd := range []string{
		"GIT_SEQUENCE_EDITOR=: git rebase -i main",
		"GIT_SEQUENCE_EDITOR=true git rebase -i main",
		"GIT_SEQUENCE_EDITOR=: git rebase --interactive HEAD~3",
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — the rebase arm REQUIRES an automated sequence editor, so an inert one must satisfy it AND clear the program screen", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_EditorCarveOut_RealProgramsScreen is the other direction the ruling authorized,
// and it is a MORE-restrictive movement: `GIT_SEQUENCE_EDITOR=<a real program>` used to
// approve and now does not, because the variable is no longer declined.
func TestGit_EditorCarveOut_RealProgramsScreen(t *testing.T) {
	// The acceptance criteria's own four rows, then the sink that motivated the ruling.
	cmds := []string{
		"GIT_EDITOR=/tmp/evil git commit --amend",
		"GIT_SEQUENCE_EDITOR=/tmp/evil git rebase -i main",
		"git -c core.editor=/tmp/evil commit --amend",
		"git -c sequence.editor=/tmp/evil rebase -i main",
		// The measured exec sink itself: pg2-6c85x ran a marker on the rebase todo.
		"GIT_SEQUENCE_EDITOR=/tmp/marker.sh git rebase -i HEAD~1",
		`GIT_SEQUENCE_EDITOR="sed -i 's/^pick /fixup /'" git rebase -i HEAD~1`,
		"GIT_EDITOR=vim git commit",
		"GIT_EDITOR=/usr/bin/true git commit --amend",
	}
	for _, cmd := range cmds {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — the carve-out is TWO inert literals, not a judgement about which programs are benign; git EXECUTES the editor value (measured, scripts/probe-pg2-6c85x.sh)", cmd, got.Reason)
		}
	}
}

// TestGit_EditorCarveOut_NearMissesDoNotClear is constraint (b): the allowance is
// EXACT-TOKEN, so nothing that merely CONTAINS, EXTENDS or is a PATH TO an inert value
// clears. Each row is a value a prefix, substring or case-folding match would have let
// through.
func TestGit_EditorCarveOut_NearMissesDoNotClear(t *testing.T) {
	values := []string{
		"truex",     // a longer token is a different program
		"true2",     //
		"tru",       // a shorter token is a different program
		"/bin/true", // a PATH the caller chose, even if today's binary is harmless
		"./true",    //
		"true.sh",   //
		`"true "`,   // a trailing space makes it a different token
		`" true"`,   //
		`"true "`,   //
		"TRUE",      // env VALUES are case-SENSITIVE (measured, pg2-6c85x)
		"True",      //
		"::",        // a longer token again
		`":;evil"`,  // `;` ends the editor and starts a command
		`": ; evil"`,
		`"true; evil"`,
		`"true && evil"`,
		`"true|evil"`,
		`"true"`, // cmdparse keeps the assignment value RAW, quotes included, so even the
		`'true'`, // quoted spelling of an allowed token fails CLOSED. Deliberate.
		"",       // an empty value names nothing statically
	}
	for name, twin := range editorSpellingPairs(t) {
		for _, value := range values {
			for _, cmd := range []string{
				envEditor(name, value, "commit --amend"),
				argvEditor(twin, value, "log"),
			} {
				if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
					t.Errorf("cmd %q: got APPROVE (%s) — the inert-value allowance is EXACT-TOKEN and value %q is not one of the two tokens; a prefix, substring or case-folding match here would be an exec sink (pg2-6qh3p constraint (b))", cmd, got.Reason, value)
				}
			}
		}
	}
	// The predicate itself, so a failure localises to the token set rather than to a
	// verdict several layers away.
	for _, value := range []string{"true", ":"} {
		if !isInertEditorValue(value) {
			t.Errorf("isInertEditorValue(%q) = false — these are exactly the two values the operator ruling allows", value)
		}
	}
	for _, value := range values {
		if isInertEditorValue(value) {
			t.Errorf("isInertEditorValue(%q) = true — only the two EXACT tokens `true` and `:` may clear (pg2-6qh3p constraint (b))", value)
		}
	}
	// And the set's own size, because a widening here is a widening of every site that
	// uses the predicate — both env variables and both argv keys at once.
	if len(inertEditorValues) != 2 {
		t.Errorf("inertEditorValues has %d members, want 2 (`true` and `:`) — this set is read by GIT_EDITOR, GIT_SEQUENCE_EDITOR, `-c core.editor=` and `-c sequence.editor=` at once, so any addition needs its own operator ruling", len(inertEditorValues))
	}
}

// TestGit_EditorCarveOut_NonLiteralValuesFailClosed is constraint (c): a value that is a
// variable, a substitution, or otherwise not a literal MUST reach the screened verdict.
//
// It asserts the verdict AND the mechanism. The verdict alone would pass for the wrong
// reason — none of these values is one of the two tokens either — so the second half
// checks cmdparse's own ExpansionNone gate directly, which is what keeps the property
// true if the token set ever grows.
func TestGit_EditorCarveOut_NonLiteralValuesFailClosed(t *testing.T) {
	nonLiteral := []string{
		"$X",
		"$(echo true)",
		"`echo true`",
		"${EDITOR:-true}",
		"${X}",
		"$TRUE_CMD",
		"$(which true)",
	}
	for name := range gitEditorEnvVars {
		for _, value := range nonLiteral {
			cmd := envEditor(name, value, "commit --amend")
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — a value that is not a LITERAL must reach the screened verdict; what it expands to is not knowable from the command text (pg2-6qh3p constraint (c))", cmd, got.Reason)
			}
			// The mechanism: cmdparse must classify these as expanding, so the carve-out
			// is unreachable for them regardless of the token comparison.
			var seen bool
			for _, pc := range cmdparse.Parse(cmd) {
				for _, ev := range pc.EnvVars {
					if ev.Name != name {
						continue
					}
					seen = true
					if ev.Expansion == cmdparse.ExpansionNone {
						t.Errorf("cmd %q: cmdparse classified value %q as ExpansionNone — the carve-out's fail-closed guard rests on this classification, so a static reading of an expanding value would make it reachable if the token set ever grew", cmd, ev.Value)
					}
				}
			}
			if !seen {
				t.Errorf("cmd %q: no %s assignment reached pc.EnvVars — the screen reads this leaf's own prefix, so a value it cannot see is a value it cannot judge", cmd, name)
			}
		}
	}
}

// TestGit_EditorCarveOut_EnvMatchesArgv is constraint (a) stated as the relation it is:
// for the SAME editor value, the env spelling and its argv twin reach the SAME verdict.
//
// This is the assertion that fails if only the env half is carved out — the mistake the
// ruling names as the most likely one — and it fails in the direction that matters,
// because pg2-6c85x's own relation tests only sample a NON-inert value.
//
// `rebase -i` IS DELIBERATELY ABSENT from the subcommand list, and its absence is
// recorded rather than left as an oversight: the rebase arm requires the ENV spelling
// specifically (hasSequenceEditorEnvVar), so for that one subcommand the argv twin is
// strictly MORE restrictive even when the `-c` is cleared. That is a different gate, it
// errs toward the prompt, and reconciling it means re-ruling the rebase carve-out itself.
// It is asserted in that direction below.
func TestGit_EditorCarveOut_EnvMatchesArgv(t *testing.T) {
	values := []string{"true", ":", "/tmp/evil", "truex", "TRUE", "vim", ""}
	for name, twin := range editorSpellingPairs(t) {
		for _, sub := range approveClassSubcommands {
			for _, value := range values {
				envCmd := envEditor(name, value, sub)
				argvCmd := argvEditor(twin, value, sub)
				envGot := evalCmd(t, envCmd)
				argvGot := evalCmd(t, argvCmd)
				if envGot.Decision != argvGot.Decision {
					t.Errorf("%s=%q, `git %s`: env spelling got %s (%s) but the -c %s spelling got %s (%s) — one editor value, two spellings, and they MUST reach the same verdict; carving out only the env half is the mistake pg2-6qh3p's ruling names (constraint (a))",
						name, value, sub, envGot.Decision, envGot.Reason, twin, argvGot.Decision, argvGot.Reason)
				}
			}
		}
	}
	// THE RECORDED EXCEPTION, asserted so it cannot drift the wrong way. For an
	// interactive rebase the argv twin is NOT less restrictive than the env spelling — it
	// is more so, because the rebase arm's editor requirement reads the env prefix.
	envGot := evalCmd(t, "GIT_SEQUENCE_EDITOR=: git rebase -i main")
	argvGot := evalCmd(t, "git -c sequence.editor=: rebase -i main")
	if argvGot.Decision < envGot.Decision {
		t.Errorf("`git -c sequence.editor=: rebase -i main` got %s (%s), which is LESS restrictive than the env spelling's %s (%s) — the argv route must never become the cheaper way past the rebase arm's editor requirement",
			argvGot.Decision, argvGot.Reason, envGot.Decision, envGot.Reason)
	}
}

// TestGit_EditorCarveOut_DoesNotWeakenADecisiveVerdict is the fail-closed half that the
// value carve-out makes newly relevant: an inert editor now clears the screen, so it must
// be shown NOT to have become a way around anything decisive.
//
// The env half cannot weaken a decisive verdict by construction — hasGitProgramEnvVar is
// a DEMOTION of an Approve. The ARGV half is the one that needs asserting, because
// clearing a `-c` removes a PRE-CLASSIFY short-circuit, so a cleared editor pair reaches
// classify where an uncleared one never did.
func TestGit_EditorCarveOut_DoesNotWeakenADecisiveVerdict(t *testing.T) {
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
			"GIT_EDITOR=true git " + row.sub,
			"GIT_EDITOR=: git " + row.sub,
			"GIT_SEQUENCE_EDITOR=true git " + row.sub,
			"git -c core.editor=true " + row.sub,
			"git -c sequence.editor=: " + row.sub,
		} {
			if got := evalCmd(t, cmd); got.Decision != row.want {
				t.Errorf("cmd %q: got %s (%s), want %s — %s; an INERT editor value is not authority to reach a weaker verdict than the bare command (pg2-6qh3p)", cmd, got.Decision, got.Reason, row.want, row.why)
			}
		}
	}
}

// TestGit_EditorCarveOut_OtherProgramVarsStayValueBlind is the containment assertion the
// ruling's constraint (b) implies but does not spell out: the value-reading departure is
// bounded to the editor family and must not leak into a general value-reading posture.
//
// The inert tokens are used as the probe values deliberately. If the carve-out ever
// leaked to another variable or key, `GIT_PAGER=true git log` would start approving, and
// nothing else in the suite would notice.
func TestGit_EditorCarveOut_OtherProgramVarsStayValueBlind(t *testing.T) {
	for name, twin := range gitProgramEnvVars {
		if gitEditorEnvVars[name] {
			continue
		}
		for _, value := range []string{"true", ":", "cat", ""} {
			for _, cmd := range []string{
				envEditor(name, value, "log"),
				argvEditor(twin, value, "log"),
			} {
				if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
					t.Errorf("cmd %q: got APPROVE (%s) — %s is NOT in the editor carve-out, and value-blindness is the measured ruling for it (see gitProgramEnvVars); the carve-out must not leak into a general value-reading posture", cmd, got.Reason, name)
				}
			}
		}
	}
}

// TestGit_EditorCarveOut_TextIsNotAnOperation is the pg2-5b901 class, re-pinned because
// this bead's own bookkeeping mentions the carved-out spellings in prose. A screened or
// carved-out spelling QUOTED in a commit message is an ARGUMENT, never an assignment.
func TestGit_EditorCarveOut_TextIsNotAnOperation(t *testing.T) {
	cmds := []string{
		`git commit -m "carve out GIT_EDITOR=true and GIT_EDITOR=: (pg2-6qh3p)"`,
		`git commit -m "GIT_SEQUENCE_EDITOR=/tmp/evil measured allow before the fix"`,
		`git commit -m "git -c sequence.editor=: is now cleared"`,
	}
	for _, cmd := range cmds {
		got := evalCmd(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — a mention in a commit message is TEXT, not an assignment (pg2-5b901)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_EditorCarveOut_EmitsExpectedHookOutput is the BOUNDARY-LEVEL assertion: the
// internal Decision cannot show what Claude Code actually receives. The carve-out's whole
// point is that these rows stop emitting `{}`, and the screen's whole point is that the
// others keep emitting it.
//
// hookio.FormatOutput is the exact function cmd/claude-extended-tool-approver's
// handlePreToolUse writes to stdout.
func TestGit_EditorCarveOut_EmitsExpectedHookOutput(t *testing.T) {
	for _, cmd := range []string{
		"GIT_EDITOR=true git rebase --continue",
		"GIT_EDITOR=: git rebase --skip",
		"GIT_SEQUENCE_EDITOR=: git rebase -i main",
		"git -c core.editor=true rebase --continue",
		"git -c sequence.editor=: rebase --continue",
	} {
		out := string(hookio.FormatOutput(evalCmd(t, cmd), nil))
		if !strings.Contains(out, `"allow"`) {
			t.Errorf("cmd %q: emitted %s, want an allow decision — an INERT editor value must not cost a prompt; `{}` here IS the 65-row friction the ruling removes", cmd, out)
		}
	}
	for _, cmd := range []string{
		"GIT_EDITOR=/tmp/evil git commit --amend",
		"GIT_SEQUENCE_EDITOR=/tmp/evil git rebase -i main",
		"git -c core.editor=/tmp/evil commit --amend",
		"git -c sequence.editor=/tmp/evil rebase -i main",
	} {
		out := string(hookio.FormatOutput(evalCmd(t, cmd), nil))
		if out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {} — git EXECUTES the editor value, so `permissionDecision: \"allow\"` would auto-approve a command that runs a program named in its own prefix", cmd, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("cmd %q: emitted %s, which contains an allow decision", cmd, out)
		}
	}
}
