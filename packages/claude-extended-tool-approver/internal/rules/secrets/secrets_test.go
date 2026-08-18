package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

func bashInput(cmd string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{ToolName: "Bash", ToolInput: ti}
}

func fileInput(tool, path string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return &hookio.HookInput{ToolName: tool, ToolInput: ti}
}

func searchInput(tool, pattern, path string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.SearchToolInput{Pattern: pattern, Path: path})
	return &hookio.HookInput{ToolName: tool, ToolInput: ti}
}

func TestRule(t *testing.T) {
	// No sandbox deny config → a secret path is Ask (not Reject).
	r := New(patheval.NewWithCWD("/project", "/project"))
	tests := []struct {
		name  string
		input *hookio.HookInput
		want  hookio.Decision
	}{
		// Bash reads of secrets → Ask
		{"cat claude credentials", bashInput("cat ~/.claude/.credentials"), hookio.Ask},
		{"cat linux credentials json", bashInput("cat ~/.claude/.credentials.json"), hookio.Ask},
		{"cat bare dotenv", bashInput("cat .env"), hookio.Ask},
		{"cat secrets json", bashInput("cat secrets/svc/prod.json"), hookio.Ask},
		{"head ssh config", bashInput("head -n 5 ~/.ssh/config"), hookio.Ask},
		// grep whose FILE arg is a secret (pattern is not) → Ask
		{"grep into ssh config", bashInput("grep Host ~/.ssh/config"), hookio.Ask},

		// False-positive avoidance (pg2-ia640.2): the grep/rg positional PATTERN,
		// grep -e pattern values, rg value-flag values, and the jq value
		// flags + bare filter are NOT secret file paths — must Abstain, not Ask.
		{"grep pattern .env is not a file", bashInput("grep .env file.log"), hookio.NoOpinion},
		{"rg pattern .env is not a file", bashInput("rg .env somefile.log"), hookio.NoOpinion},
		{"grep -e .env pattern value is not a file", bashInput("grep -e .env file.log"), hookio.NoOpinion},
		{"rg -g glob value is not a file", bashInput("rg -g '*.env' pattern file.log"), hookio.NoOpinion},

		// `-f`/`--file` IS A READ, and this row asserted the opposite until pg2-ygjs5.
		// It is the one pg2-ia640.2 row that changed verdict, deliberately: `-f FILE` is
		// grep's PATTERN FILE, so grep OPENS it and its contents become the patterns.
		// Grouping it with `-e` was the mistake — `-e`'s operand is the pattern ITSELF
		// and is never opened, while `-f`'s operand is a file — and treating the two
		// alike is what made `grep -f ~/.ssh/id_rsa x.log` auto-approve while the
		// positional control `grep pat ~/.ssh/id_rsa` rejected. Unlike its siblings
		// above, this row was synthetic table coverage rather than an observed asklog
		// invocation, so no measured false positive is reintroduced by flipping it.
		{"grep -f .env READS .env as the pattern file", bashInput("grep -f .env file.log"), hookio.Ask},
		{"jq --arg value .env is not a file", bashInput("jq --arg x .env '.'"), hookio.NoOpinion},
		{"jq bare filter .credentials is not a file", bashInput("jq '.credentials' data.json"), hookio.NoOpinion},

		// Regression — a real secret FILE arg still Asks (pattern/filter exemption
		// must not suppress the actual secret file reference).
		{"grep password into dotenv FILE", bashInput("grep password .env"), hookio.Ask},
		{"jq token filter over auth.json FILE", bashInput("jq '.token' auth.json"), hookio.Ask},
		// stdin redirect read of a secret must not bypass the check
		{"cat stdin-redirect from secrets", bashInput("cat < secrets/prod.json"), hookio.Ask},
		// sh/bash -c '<inner>' must not bypass the check
		{"bash -c cat dotenv", bashInput("bash -c 'cat .env'"), hookio.Ask},
		{"sh -c cat credentials", bashInput("sh -c \"cat ~/.claude/.credentials\""), hookio.Ask},
		// Combined single-dash short-flag groups ending in `c` (bash -lc, sh -ilc)
		// are also `-c` wrappers — the inner command is the NEXT token and must be
		// scanned (pg2-ia640.4).
		{"bash -lc cat dotenv", bashInput("bash -lc 'cat .env'"), hookio.Ask},
		{"bash -ilc cat credentials", bashInput("bash -ilc 'cat ~/.claude/.credentials'"), hookio.Ask},
		{"sh -ilc cat secrets json", bashInput("sh -ilc 'cat secrets/prod.json'"), hookio.Ask},
		// env exec-prefix is unwrapped by cmdparse, so the combined-flag wrapper
		// inside it is still scanned (regression guard for the env path).
		{"env bash -lc cat dotenv", bashInput("env bash -lc 'cat .env'"), hookio.Ask},
		// Nested combined-flag wrappers recurse within the maxShellUnwrap cap.
		{"nested bash -lc sh -lc cat dotenv", bashInput("bash -lc 'sh -lc \"cat .env\"'"), hookio.Ask},
		// OVER-MATCH GUARD: a `--` long option that merely contains `c` must NOT be
		// treated as a `-c` wrapper (else its following token — the rcfile path — is
		// wrongly scanned as an inner command).
		{"bash --rcfile not a wrapper", bashInput("bash --rcfile ~/.bashrc"), hookio.NoOpinion},
		// A combined-flag wrapper whose inner command reads no secret must not
		// over-fire.
		{"bash -lc echo hi", bashInput("bash -lc 'echo hi'"), hookio.NoOpinion},

		// False-positive avoidance (pg2-ia640.5): a FREE-TEXT MESSAGE value is
		// stored as text, never opened, so a credential path merely NAMED inside
		// one is not a reference — must Abstain, so the safe-command approval of
		// `bd` (safecmds' "bd": true) is reached instead of a prompt.
		{"bd close --reason prose", bashInput(`bd close pg2-x --reason "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"bd close --reason=prose (equals spelling)", bashInput(`bd close pg2-x --reason="cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"bd create --title prose", bashInput(`bd create --title "SECURITY: ~/.ssh/agent glob probe"`), hookio.NoOpinion},
		{"bd create --description prose", bashInput(`bd create --description "names a/secrets/prod.yaml"`), hookio.NoOpinion},
		{"bd update --append-notes prose", bashInput(`bd update pg2-x --append-notes "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"bd comment body positional prose", bashInput(`bd comment pg2-x "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"git commit -m prose", bashInput(`git commit -m "drop the docs/secrets/prod.yaml example"`), hookio.NoOpinion},
		{"git commit --message prose", bashInput(`git commit --message "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"git -C dir commit -m prose", bashInput(`git -C /repo commit -m "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"gh pr comment --body prose", bashInput(`gh pr comment 1 --body "cert probe via ~/.ssh/agent glob"`), hookio.NoOpinion},
		{"gh issue create -t/-b prose", bashInput(`gh issue create -t "fix ~/.ssh/agent glob" -b "body a/secrets/x"`), hookio.NoOpinion},

		// ANTI-BYPASS GUARDS for the same carve-out. Each of these Asks TODAY and
		// must keep Asking; between them they pin every boundary the carve-out
		// claims (see cmdparse.SkipMessageArgs and secretCandidateArgs).
		//
		// The skip is keyed on an ENUMERATED set of executables, so a command that
		// OPENS its arguments is unaffected even with a message-shaped flag.
		{"cp is not in the skip table", bashInput("cp ~/.ssh/id_rsa /tmp"), hookio.Ask},
		{"unlisted command with a --reason flag", bashInput(`notes-tool --reason ~/.ssh/id_rsa`), hookio.Ask},
		// The file-taking flags read the message FROM a path, so their VALUE stays
		// a candidate. `git commit -F` is the most important of these: it passes
		// only because -F is absent from the message table — a "drop the token
		// after any listed flag" implementation breaks it silently.
		{"git commit -F path still Asks", bashInput("git commit -F ~/.ssh/id_rsa"), hookio.Ask},
		{"git commit --file path still Asks", bashInput("git commit --file ~/.ssh/id_rsa"), hookio.Ask},
		{"gh pr create --body-file path still Asks", bashInput("gh pr create --body-file ~/.ssh/id_rsa"), hookio.Ask},
		{"bd comment --file path still Asks", bashInput("bd comment x --file secrets/notes.txt"), hookio.Ask},
		// `-m` is BOOLEAN outside gitMessageSubcommands, so the token after it is a
		// real pathspec there (git checkout -m = --merge).
		{"git checkout -m pathspec still Asks", bashInput("git checkout -m ~/.ssh/config"), hookio.Ask},
		// After a bare `--` a token is an operand, not a flag, so a path cannot be
		// hidden by prefixing it with a message flag name.
		{"git commit -- -m path still Asks", bashInput("git commit -- -m ~/.ssh/id_rsa"), hookio.Ask},
		// Only the BODY positional of `bd comment <id> <body>` is dropped.
		{"bd comment id positional still Asks", bashInput("bd comment ~/.ssh/id_rsa body"), hookio.Ask},
		{"bd -C dir value still Asks", bashInput("bd -C secrets/wt comment x body"), hookio.Ask},
		// Getting the FILE'S CONTENT into a message is a different construct, and
		// each is still checked: a redirection, and a shell -c wrapper.
		{"bd comment with stdin redirect still Asks", bashInput("bd comment x body < secrets/x"), hookio.Ask},
		{"bash -lc inside a bd comment body still Asks", bashInput(`bd comment x "$(echo hi)" && bash -lc 'cat ~/.ssh/id_rsa'`), hookio.Ask},

		// Bash without a secret path → Abstain (defer to rest of chain)
		{"cat readme", bashInput("cat README.md"), hookio.NoOpinion},
		{"echo hello", bashInput("echo hello"), hookio.NoOpinion},
		{"ls tmp", bashInput("ls /tmp"), hookio.NoOpinion},
		// bare "secrets" word (kubectl subcommand) must NOT be flagged
		{"kubectl get secrets not flagged", bashInput("kubectl get secrets"), hookio.NoOpinion},

		// File tools
		{"Read credentials", fileInput("Read", "~/.claude/.credentials"), hookio.Ask},
		{"Read normal file", fileInput("Read", "internal/main.go"), hookio.NoOpinion},
		{"Write to secret", fileInput("Write", "~/.ssh/authorized_keys"), hookio.Ask},
		{"Edit normal file", fileInput("Edit", "src/app.go"), hookio.NoOpinion},

		// Search tools
		{"Grep in secrets dir", searchInput("Grep", "password", "secrets/"), hookio.Ask},
		{"Grep normal dir", searchInput("Grep", "TODO", "internal/"), hookio.NoOpinion},
		{"Glob no path", searchInput("Glob", "**/*.go", ""), hookio.NoOpinion},

		// Unrelated tools → Abstain
		{"WebFetch", &hookio.HookInput{ToolName: "WebFetch"}, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(tt.input))
			if got.Decision != tt.want {
				t.Errorf("Evaluate(%s) decision = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
			if got.Decision != hookio.NoOpinion && got.Module != r.Name() {
				t.Errorf("Evaluate(%s) module = %q, want %q", tt.name, got.Module, r.Name())
			}
		})
	}
}

// TestRule_DenyListedSecretRejects verifies that when a secret path is ALSO
// deny-listed the rule returns Reject (preserving the hard block path-safety
// would give, since this rule runs before path-safety) rather than downgrading
// it to Ask.
func TestRule_DenyListedSecretRejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/testuser/.ssh"},
	})
	r := New(pe)
	tests := []struct {
		name  string
		input *hookio.HookInput
		want  hookio.Decision
	}{
		{"Read deny-listed ssh key", fileInput("Read", "/Users/testuser/.ssh/id_rsa"), hookio.Reject},
		{"cat deny-listed ssh key", bashInput("cat /Users/testuser/.ssh/id_rsa"), hookio.Reject},
		// A secret NOT under the deny path still only prompts.
		{"Read non-denied secret", fileInput("Read", "/Users/testuser/.claude/.credentials"), hookio.Ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookio.Verdict(r.Evaluate(tt.input)); got.Decision != tt.want {
				t.Errorf("Evaluate(%s) = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestRule_GluedQuoteParity is pg2-6f2gu's relation-fixture requirement, the
// secrets-rule analog of gh's TestGH_Api_GluedQuoteParity (pg2-9zgso): a value
// GLUED to an unquoted flag name AND quoted (`--file='X'`) must reach the SAME
// verdict as the unquoted glued spelling (`--file=X`) and the space-separated
// spelling (`--file X`) — all three are identical to the shell, and pg2-cu3ro's
// own acceptance criteria already pinned the first two as a relation; this adds
// the third spelling cmdparse.GluedFlagValue never stripped quotes from.
//
// It runs with a DenyRead sandbox config so the ssh-key row exercises the Reject
// branch (configRef/denyListed), not just Ask — the branch the audit's own
// example (`cat --file='~/.ssh/id_rsa'`) singled out, and MEASURED to regress to
// Ask pre-fix once the path is actually deny-listed (an unconfigured `~/.ssh/…`
// happens to still classify correctly even quoted, because its secret-directory
// match lands on a MIDDLE path segment the quotes never touch — see the decision
// comment at firstSecretRef's GluedFlagValue call for why that one example is not
// a reliable witness on its own).
func TestRule_GluedQuoteParity(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/testuser/.ssh"},
	})
	r := New(pe)

	cases := []struct {
		prefix, flag, path string
		want               hookio.Decision
	}{
		// Deny-listed: the basename/prefix comparison this fix repairs.
		{"cat", "--file", "/Users/testuser/.ssh/id_rsa", hookio.Reject},
		{"git commit", "--file", "/Users/testuser/.ssh/id_rsa", hookio.Reject},
		// Secret-shaped but not deny-listed: classification hinges on the LAST
		// segment (basename) or the WHOLE string (no "/" at all) — exactly the
		// arms a boundary quote corrupts.
		{"cat", "--file", "secrets/notes.txt", hookio.Ask},
		{"cat", "--file", ".env", hookio.Ask},
		{"cat", "--file", "auth.json", hookio.Ask},
	}
	for _, c := range cases {
		spaced := c.prefix + " " + c.flag + " " + c.path
		gluedPlain := c.prefix + " " + c.flag + "=" + c.path
		gluedQuoted := c.prefix + " " + c.flag + "='" + c.path + "'"

		sv := hookio.Verdict(r.Evaluate(bashInput(spaced)))
		gv := hookio.Verdict(r.Evaluate(bashInput(gluedPlain)))
		qv := hookio.Verdict(r.Evaluate(bashInput(gluedQuoted)))

		if gv.Decision != sv.Decision {
			t.Errorf("GLUED-SPELLING DISAGREEMENT: %q is %s but %q is %s (pg2-cu3ro)",
				gluedPlain, gv.Decision, spaced, sv.Decision)
		}
		if qv.Decision != sv.Decision {
			t.Errorf("GLUED-QUOTE DISAGREEMENT: %q is %s (%s) but %q is %s (%s) — both are identical to the shell and MUST reach the same verdict (pg2-6f2gu)",
				gluedQuoted, qv.Decision, qv.Reason, spaced, sv.Decision, sv.Reason)
		}
		// Pin the DIRECTION too, not just the relation — all three spellings must
		// agree by both reaching the CORRECT verdict, not by all three regressing.
		for _, tt := range []struct {
			cmd string
			got hookio.Decision
		}{
			{spaced, sv.Decision}, {gluedPlain, gv.Decision}, {gluedQuoted, qv.Decision},
		} {
			if tt.got != c.want {
				t.Errorf("cmd %q: got %s, want %s", tt.cmd, tt.got, c.want)
			}
		}
	}
}

// TestRule_MalformedGluedQuotingDoesNotRegress pins pg2-6f2gu's fail-closed
// requirement: a glued value whose quoting is malformed — an interior wrapper
// character (multi-segment concatenation), an unterminated quote, or a
// double-wrapped value — is NOT a clean unwrap, so cmdparse.UnwrapGluedQuotes
// declines and hands firstSecretRef back the value EXACTLY as cmdparse produced
// it (its own documented residual-case behavior, pg2-9zgso). Each fixture below
// uses ".env" — a basename-only secret with no "/" at all — specifically so
// there is no directory-component coincidence to muddy the read (contrast the
// GluedQuoteParity fixtures above, whose ssh-key row is a "/"-separated path):
// every one of these malformed spellings is Abstain, identical to before this
// fix, because a value that fails to unwrap cleanly is never a clean match
// either — declining is a no-op on these exact bytes, so the verdict cannot
// change whether or not the unwrap call is present.
func TestRule_MalformedGluedQuotingDoesNotRegress(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	r := New(pe)

	tests := []struct {
		name string
		cmd  string
	}{
		{
			"interior contains the wrapper character (multi-segment concatenation)",
			"cat --file='.env'x'.env'",
		},
		{
			"unterminated: only one quote, at the start",
			"cat --file='.env",
		},
		{
			"double-wrapped: outer pair around an already-quoted inner value",
			"cat --file=''.env''",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.cmd)))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain — a malformed glued quote must not be treated as a clean unwrap", tt.cmd, got.Decision, got.Reason)
			}
		})
	}
}

// asklogRow returns the VERBATIM command text of an observed asklog row, held in
// testdata so the fixture is byte-exact rather than a Go-escaped paraphrase (the
// bodies are multi-line and contain quotes, backticks and the `'"'"'` shell
// idiom, none of which survive retyping). Only the trailing newline the export
// added is removed.
func asklogRow(t *testing.T, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "asklog-row-"+id+".cmd"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n")
}

// unfilteredArgsHaveSecret reports whether any argument of any leaf of cmd is
// lexically secret — i.e. exactly what the PRE-carve-out candidate set (an
// unfiltered pc.Args, minus the `-`-prefixed tokens firstSecretRef skips) handed
// to secretpath.IsSecret.
//
// It is the PRECONDITION of every fixture below, and it is what stops those
// fixtures from passing for the wrong reason: a command whose prose no longer
// names a credential path would Abstain with or without the carve-out, so the
// test would keep passing while testing nothing.
func unfilteredArgsHaveSecret(cmd string) bool {
	for _, pc := range cmdparse.Parse(cmd) {
		for _, a := range pc.Args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if secretpath.IsSecret(a) {
				return true
			}
		}
	}
	return false
}

// The four observed asklog rows of pg2-ia640.5, replayed VERBATIM: a `bd` command
// whose free-text body/reason/title/description merely MENTIONS a `.ssh/` or
// `secrets/` path reads, writes and executes nothing, so this rule must Abstain
// and let the chain reach safecmds, where `bd` is allow-listed.
func TestRule_ObservedAsklogRows_MessageProseAbstains(t *testing.T) {
	r := New(patheval.NewWithCWD("/project", "/project"))
	for _, tt := range []struct {
		row  string
		what string
	}{
		{"313634", "bd close --reason (the close of pg2-ia640.2, the bead that fixed the PREVIOUS instance)"},
		{"325419", "bd comment <id> <~40-line body naming ~/.ssh/agent>"},
		{"325591", "bd create --title/--description (multi-line, backslash-continued)"},
		{"325750", "bd comment <id> <prose> inside a pipeline"},
	} {
		t.Run("row "+tt.row, func(t *testing.T) {
			cmd := asklogRow(t, tt.row)
			if !unfilteredArgsHaveSecret(cmd) {
				t.Fatalf("precondition: row %s no longer carries a lexically secret argument, so it cannot exercise the carve-out (%s)", tt.row, tt.what)
			}
			got := hookio.Verdict(r.Evaluate(bashInput(cmd)))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("row %s (%s) = %v, want abstain (reason %q)", tt.row, tt.what, got.Decision, got.Reason)
			}
		})
	}
}

// The EQUALS spelling of a message flag must never be LESS restrictive than the
// space-separated spelling of the same flag with the same value. Stated as a
// RELATION rather than as two hardcoded verdicts so it survives retuning of what
// either spelling decides.
//
// The relation is one-directional on purpose. Equal is the intended outcome here,
// but `hookio.Decision` is ordered by restrictiveness (Approve < NoOpinion < Ask <
// Reject), and only "equals is not the weaker spelling" is a safety property.
func TestRule_MessageCarveOut_EqualsSpellingNotWeaker(t *testing.T) {
	r := New(patheval.NewWithCWD("/project", "/project"))
	for _, c := range []struct{ flag, value, prefix string }{
		{"--reason", "cert probe via ~/.ssh/agent glob", "bd close pg2-x"},
		{"--description", "names a/secrets/prod.yaml", "bd create"},
		{"--title", "SECURITY: ~/.ssh/agent glob", "bd create"},
		{"--append-notes", "cert probe via ~/.ssh/agent glob", "bd update pg2-x"},
		{"--message", "cert probe via ~/.ssh/agent glob", "git commit"},
		{"--body", "cert probe via ~/.ssh/agent glob", "gh pr comment 1"},
	} {
		t.Run(c.prefix+" "+c.flag, func(t *testing.T) {
			spaced := hookio.Verdict(r.Evaluate(bashInput(c.prefix + " " + c.flag + ` "` + c.value + `"`)))
			equals := hookio.Verdict(r.Evaluate(bashInput(c.prefix + " " + c.flag + `="` + c.value + `"`)))
			if equals.Decision < spaced.Decision {
				t.Errorf("%s %s: equals spelling = %v is LESS restrictive than the space spelling = %v",
					c.prefix, c.flag, equals.Decision, spaced.Decision)
			}
		})
	}
}

// The carve-out hides a message VALUE, not a command. `bd comment x
// "$(cat ~/.ssh/id_rsa)"` reads the key, and what stops it is the ENGINE: it
// enumerates every top-level substitution body and recurses it through ALL rules
// as its own evaluation unit (engine.foldSubstitutionScan), folding
// MostRestrictive. This test pins both halves of that at the seam this package can
// reach — the body IS enumerated by the scanner the engine uses, and this rule
// Asks on it — so the guard cannot be lost by a change to either side.
func TestRule_CommandSubstitutionInsideAMessageStillReachesTheRule(t *testing.T) {
	const cmd = `bd comment x "$(cat ~/.ssh/id_rsa)"`
	scan := cmdparse.ScanSubstitutions(cmd)
	if scan.Unparseable {
		t.Fatalf("precondition: %q did not parse (%s)", cmd, scan.Reason)
	}
	if len(scan.Substitutions) != 1 {
		t.Fatalf("ScanSubstitutions(%q) found %d substitutions, want 1 — the engine recurses what this enumerates", cmd, len(scan.Substitutions))
	}
	body := scan.Substitutions[0].Body
	r := New(patheval.NewWithCWD("/project", "/project"))
	got := hookio.Verdict(r.Evaluate(bashInput(body)))
	if got.Decision != hookio.Ask {
		t.Errorf("substitution body %q = %v, want ask (reason %q)", body, got.Decision, got.Reason)
	}
	// And the message value itself is inert text, so the leaf abstains — which is
	// precisely why the substitution recursion is what carries the guard.
	if leaf := hookio.Verdict(r.Evaluate(bashInput(cmd))); leaf.Decision != hookio.NoOpinion {
		t.Errorf("leaf %q = %v, want abstain — the body positional is a message value (reason %q)", cmd, leaf.Decision, leaf.Reason)
	}
}

// NO MULTI-LINE ESCAPE HATCH. The carve-out is keyed on POSITION (a named flag's
// value, or bd comment's body positional), never on an argument "looking like
// prose". A multi-line argument in any other position still Asks — which is the
// bound on the newline backstop cmdparse.SkipMessageArgs declines, and the reason
// declining it costs nothing: a newline is not evidence of prose.
func TestRule_MultilineArgumentIsNotItselfAProseSignal(t *testing.T) {
	r := New(patheval.NewWithCWD("/project", "/project"))
	const prose = "line one\nsecond line names ~/.ssh/agent\nthird line"
	for _, tt := range []struct {
		name string
		cmd  string
		want hookio.Decision
	}{
		// Unlisted executable: multi-line changes nothing.
		{"unlisted command", `notes-tool "` + prose + `"`, hookio.Ask},
		// Listed executable, UNLISTED position: bd create's POSITIONAL title is
		// not in the enumerated set, so it keeps today's behaviour.
		{"bd create positional title", `bd create "` + prose + `"`, hookio.Ask},
		// A path with a newline appended must not launder itself into "prose".
		{"path plus a newline", "cat \"~/.ssh/id_rsa\n\"", hookio.Ask},
		// The enumerated position DOES get the relief, multi-line included — that
		// is the whole point of the bead (row 325419 is a ~40-line body).
		{"bd comment body positional", `bd comment pg2-x "` + prose + `"`, hookio.NoOpinion},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookio.Verdict(r.Evaluate(bashInput(tt.cmd))); got.Decision != tt.want {
				t.Errorf("%s = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// linkFixture builds a credential-directory fixture in a temp dir and returns the
// fixture root. It is deliberately NOT under the project root the rule is
// constructed with: every resolution these tests assert therefore lands OUTSIDE
// the workspace, which pins the decision recorded in pathRef — a resolved path is
// classified wherever it lands, with no zone check.
//
//	<root>/.ssh/id_rsa        real credential file
//	<root>/mykeys/id_rsa      -> <root>/.ssh/id_rsa   (leaf symlink)
//	<root>/keydir             -> <root>/.ssh          (directory symlink)
//	<root>/notes/README.md    real non-credential file
//	<root>/readme-link        -> <root>/notes/README.md
func linkFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".ssh", "mykeys", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(".ssh", "id_rsa"), filepath.Join("notes", "README.md")} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ln := range [][2]string{
		{filepath.Join(root, ".ssh", "id_rsa"), filepath.Join(root, "mykeys", "id_rsa")},
		{filepath.Join(root, ".ssh"), filepath.Join(root, "keydir")},
		{filepath.Join(root, "notes", "README.md"), filepath.Join(root, "readme-link")},
	} {
		if err := os.Symlink(ln[0], ln[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Precondition: the SPELLINGS under test are not lexically secret, so a hit
	// can only come from the resolved form. Without this the tests could pass for
	// the wrong reason (e.g. a temp dir whose name contains a secret component).
	for _, p := range []string{
		filepath.Join(root, "mykeys", "id_rsa"),
		filepath.Join(root, "keydir", "id_rsa"),
		filepath.Join(root, "readme-link"),
	} {
		if secretpath.IsSecret(p) {
			t.Fatalf("precondition: %s is already lexically secret, so the resolved form is not what is being tested", p)
		}
	}
	return root
}

// A symlink pointing INTO a credential directory must be detected — the defect
// this pass exists to close (~/mykeys/id_rsa -> ~/.ssh/id_rsa), in both the leaf
// and directory-symlink shapes, and for every tool surface the rule covers.
func TestRule_SecretViaSymlink_Ask(t *testing.T) {
	root := linkFixture(t)
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))

	leaf := filepath.Join(root, "mykeys", "id_rsa")
	viaDir := filepath.Join(root, "keydir", "id_rsa")
	tests := []struct {
		name  string
		input *hookio.HookInput
	}{
		{"Read leaf symlink into .ssh", fileInput("Read", leaf)},
		{"Read through symlinked .ssh dir", fileInput("Read", viaDir)},
		{"Write leaf symlink into .ssh", fileInput("Write", leaf)},
		{"Grep under symlinked .ssh dir", searchInput("Grep", "PRIVATE", filepath.Join(root, "keydir"))},
		{"cat leaf symlink into .ssh", bashInput("cat " + leaf)},
		{"cat redirect from leaf symlink", bashInput("cat < " + leaf)},
		{"bash -lc cat leaf symlink", bashInput("bash -lc 'cat " + leaf + "'")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(tt.input))
			if got.Decision != hookio.Ask {
				t.Errorf("Evaluate(%s) = %v, want ask (reason %q)", tt.name, got.Decision, got.Reason)
			}
			// The reason must name the indirection, else the asklog records a
			// prompt for a path that does not look like a secret.
			if !strings.Contains(got.Reason, " -> ") {
				t.Errorf("Evaluate(%s) reason %q does not name the resolved target", tt.name, got.Reason)
			}
		})
	}
}

// The NAMED-form check must survive the addition of the resolved form: a
// credential file that is ITSELF a symlink still matches, even though resolving
// it moves the path OUT of the credential directory.
func TestRule_CredentialFileIsItselfASymlink_Ask(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "backup-key.pem")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(root, ".ssh", "id_rsa")
	if err := os.Symlink(target, named); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))

	// Precondition: the resolved form is NOT secret, so only the named check can
	// produce the Ask below.
	if secretpath.IsSecret(target) {
		t.Fatalf("precondition: resolved target %s is lexically secret, so this does not test the named form", target)
	}
	for _, tt := range []struct {
		name  string
		input *hookio.HookInput
	}{
		{"Read", fileInput("Read", named)},
		{"cat", bashInput("cat " + named)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(tt.input))
			if got.Decision != hookio.Ask {
				t.Errorf("Evaluate(%s) = %v, want ask (reason %q)", tt.name, got.Decision, got.Reason)
			}
			// A named-form hit reports the name alone — byte-identical to what
			// this rule emitted before the resolving pass existed.
			if strings.Contains(got.Reason, " -> ") {
				t.Errorf("Evaluate(%s) reason %q names a resolution; the named form matched", tt.name, got.Reason)
			}
		})
	}
}

// A non-credential symlink must be unaffected — the resolving pass must not turn
// every link into a prompt.
func TestRule_NonCredentialSymlink_Abstain(t *testing.T) {
	root := linkFixture(t)
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))
	link := filepath.Join(root, "readme-link")
	for _, tt := range []struct {
		name  string
		input *hookio.HookInput
	}{
		{"Read", fileInput("Read", link)},
		{"cat", bashInput("cat " + link)},
		{"Grep in notes dir", searchInput("Grep", "TODO", filepath.Join(root, "notes"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookio.Verdict(r.Evaluate(tt.input)); got.Decision != hookio.NoOpinion {
				t.Errorf("Evaluate(%s) = %v, want abstain (reason %q)", tt.name, got.Decision, got.Reason)
			}
		})
	}
}

// A nil PathEvaluator is a supported configuration (it makes the rule
// cwd-independent). It MUST NOT panic, the named-form check MUST still run, and
// the resolved-form check simply never matches.
func TestRule_NilEvaluator_NamedFormStillRuns(t *testing.T) {
	root := linkFixture(t)
	r := New(nil)
	tests := []struct {
		name  string
		input *hookio.HookInput
		want  hookio.Decision
	}{
		// Named form — unchanged by the nil evaluator.
		{"Read ssh key", fileInput("Read", "~/.ssh/id_rsa"), hookio.Ask},
		{"cat dotenv", bashInput("cat .env"), hookio.Ask},
		{"Grep secrets dir", searchInput("Grep", "password", "secrets/"), hookio.Ask},
		{"Read normal file", fileInput("Read", "internal/main.go"), hookio.NoOpinion},
		// Resolved form — unavailable without an evaluator, so it degrades to the
		// pre-pass behavior (Abstain) rather than panicking.
		{"Read symlink into .ssh", fileInput("Read", filepath.Join(root, "mykeys", "id_rsa")), hookio.NoOpinion},
		{"cat symlink into .ssh", bashInput("cat " + filepath.Join(root, "mykeys", "id_rsa")), hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(tt.input))
			if got.Decision != tt.want {
				t.Errorf("Evaluate(%s) = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// BLAST-RADIUS BOUND: the resolving pass tests only PATH-SHAPED candidates, so a
// bare word is never absolutized into a file in the cwd. `kubectl get secrets`
// with a real ./secrets directory present is the case that would regress
// (pg2-ia640.2's false-positive class); a path-shaped spelling of the same
// reference is still caught.
func TestRule_BareWordNotResolved_Abstain(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(patheval.NewWithCWD(project, project))
	if got := hookio.Verdict(r.Evaluate(bashInput("kubectl get secrets"))); got.Decision != hookio.NoOpinion {
		t.Errorf("kubectl get secrets = %v, want abstain (reason %q)", got.Decision, got.Reason)
	}
	// The same directory named path-shaped IS a hit — via the named form, since
	// `./secrets/prod.json` is already lexically secret.
	if got := hookio.Verdict(r.Evaluate(bashInput("cat ./secrets/prod.json"))); got.Decision != hookio.Ask {
		t.Errorf("cat ./secrets/prod.json = %v, want ask (reason %q)", got.Decision, got.Reason)
	}
}

// BLAST-RADIUS BOUND: resolution is capped at maxResolutions per Evaluate so a
// long argument list cannot turn the check into a stat storm. Past the cap the
// rule falls back to the lexical behavior that shipped before this pass, which is
// what the second half asserts.
func TestRule_ResolutionBudgetBounded(t *testing.T) {
	root := linkFixture(t)
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))
	link := filepath.Join(root, "mykeys", "id_rsa")

	pad := make([]string, 0, maxResolutions+1)
	for i := 0; i <= maxResolutions; i++ {
		pad = append(pad, filepath.Join(project, "pad", "f"+strconv.Itoa(i)+".txt"))
	}
	padding := strings.Join(pad, " ")

	if got := hookio.Verdict(r.Evaluate(bashInput("cat " + link + " " + padding))); got.Decision != hookio.Ask {
		t.Errorf("symlink within budget = %v, want ask (reason %q)", got.Decision, got.Reason)
	}
	if got := hookio.Verdict(r.Evaluate(bashInput("cat " + padding + " " + link))); got.Decision != hookio.NoOpinion {
		t.Errorf("symlink past the %d-resolution cap = %v, want abstain — the cap is the documented bound (reason %q)",
			maxResolutions, got.Decision, got.Reason)
	}
	// The lexical pass is NOT capped: a named secret past the same padding still
	// hits, so the cap can never cost more than the resolved-form refinement.
	if got := hookio.Verdict(r.Evaluate(bashInput("cat " + padding + " " + filepath.Join(root, ".ssh", "id_rsa")))); got.Decision != hookio.Ask {
		t.Errorf("named secret past the cap = %v, want ask (reason %q)", got.Decision, got.Reason)
	}
}

// ===========================================================================
// pg2-pmk9q — the IN-REPO relaxation of the bare `secrets` component.
// ===========================================================================

// repoScopeFixture builds one tree holding every shape the pg2-pmk9q ruling
// distinguishes, so the matching and non-matching cases are adjacent in one place
// and cannot drift apart:
//
//	<root>/secrets/prod.env             a credential store OUTSIDE any repo
//	<root>/repo/.git/                   repo marker (directory form)
//	<root>/repo/internal/rules/secrets/secrets.go   the reported false positive
//	<root>/repo/deploy/secrets/token    the operator-OVERRIDDEN guard
//	<root>/repo/.env                    repo-blind `.env` arm
//	<root>/repo/secrets/.ssh/id_rsa     both arms match; the stronger must win
//	<root>/repo/config/api-token.json   repo-blind basename arm
//	<root>/wt/.git                      repo marker (FILE form — a git worktree)
//	<root>/wt/secrets/token             same relaxation, reached via a worktree
func repoScopeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{
		filepath.Join("secrets", "prod.env"),
		filepath.Join("deploy", "secrets", "token"),
		filepath.Join("repo", "internal", "rules", "secrets", "secrets.go"),
		filepath.Join("repo", "deploy", "secrets", "token"),
		filepath.Join("repo", ".env"),
		filepath.Join("repo", "secrets", ".ssh", "id_rsa"),
		filepath.Join("repo", "config", "api-token.json"),
		filepath.Join("repo", "README.md"),
		filepath.Join("wt", "secrets", "token"),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A WORKTREE's `.git` is a FILE holding a `gitdir:` pointer, not a directory.
	// Agents in this workspace work almost exclusively in `.worktrees/<name>`
	// checkouts, so a dir-only repo test would report every one of them as
	// unversioned and the relaxation would never fire where it is needed most.
	if err := os.WriteFile(filepath.Join(root, "wt", ".git"), []byte("gitdir: "+filepath.Join(root, "repo", ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PRECONDITIONS. Without these the table below could pass for the wrong
	// reason — e.g. a temp root that happens to sit inside some git repo would
	// make the "outside a repo" rows relax too, and they would then be asserting
	// nothing.
	if patheval.InGitRepo(filepath.Join(root, "secrets", "prod.env")) {
		t.Fatalf("precondition: %s is inside a git repository, so the outside-a-repo rows cannot be tested here", filepath.Join(root, "secrets"))
	}
	for _, p := range []string{
		filepath.Join(root, "repo", "internal", "rules", "secrets", "secrets.go"),
		filepath.Join(root, "repo", "deploy", "secrets", "token"),
		filepath.Join(root, "wt", "secrets", "token"),
	} {
		if !patheval.InGitRepo(p) {
			t.Fatalf("precondition: %s is NOT inside a git repository, so the in-repo rows cannot be tested here", p)
		}
		if secretpath.Classify(p) != secretpath.GenericSecretsDir {
			t.Fatalf("precondition: Classify(%s) = %v, want GenericSecretsDir — only that arm is relaxed, so another Kind means the row tests something else",
				p, secretpath.Classify(p))
		}
	}
	return root
}

// The pg2-pmk9q ruling, pinned in both directions. A READ under a bare `secrets/`
// component INSIDE a git repository stops prompting; every other shape keeps the
// verdict it had.
//
// GUARD 2 IS DELIBERATELY OVERRIDDEN HERE. pg2-pmk9q pinned
// `deploy/secrets/token` as a NON-NEGOTIABLE regression guard that must keep
// Asking. The operator OVERRODE it on 2026-08-13, with the guard's text in front of
// them: a `deploy/` tree is inside a repo, so under the new model the read abstains
// and covering such a tree becomes a project-level `.claude/settings.json` denyRead
// entry (which patheval.LoadSandboxFilesystemConfig already merges). This is the
// ONE coverage reduction in the ruling. The row below asserts the OVERRIDDEN
// behaviour on purpose — it is not a stale expectation, and re-tightening it needs
// a new ruling, not a test edit.
func TestRule_GenericSecretsComponentSkippedInsideAGitRepo(t *testing.T) {
	root := repoScopeFixture(t)
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))
	tests := []struct {
		name string
		path string
		read hookio.Decision
		// write is the SAME path via a write tool. Reads and writes MUST stay
		// distinguished (pg2-pmk9q guard 3), and the relaxation is read-only.
		write hookio.Decision
	}{
		// RELAXED — the reported false positive, and the general case it subsumes.
		{"this rule's own source tree", filepath.Join(root, "repo", "internal", "rules", "secrets", "secrets.go"), hookio.NoOpinion, hookio.Ask},
		{"deploy/secrets/token (guard 2, operator-OVERRIDDEN)", filepath.Join(root, "deploy", "secrets", "token"), hookio.Ask, hookio.Ask},
		{"in-repo deploy/secrets/token", filepath.Join(root, "repo", "deploy", "secrets", "token"), hookio.NoOpinion, hookio.Ask},
		{"in-WORKTREE secrets/token (.git is a FILE)", filepath.Join(root, "wt", "secrets", "token"), hookio.NoOpinion, hookio.Ask},

		// NOT RELAXED, adjacent — GUARD 1: outside any repo the arm still fires.
		{"guard 1: ~/secrets/prod.env outside a repo", filepath.Join(root, "secrets", "prod.env"), hookio.Ask, hookio.Ask},

		// NOT RELAXED — the repo-BLIND arms (ruling decisions 2 and 3's bound). A
		// `.env` in a repo is the most common real credential file an agent reads,
		// and `.ssh` / `*token*.json` name a specific credential store or file, so
		// none of them is scoped by the repo test.
		{"repo .env stays lexical and repo-blind", filepath.Join(root, "repo", ".env"), hookio.Ask, hookio.Ask},
		{"in-repo secrets/.ssh/id_rsa — stronger arm wins", filepath.Join(root, "repo", "secrets", ".ssh", "id_rsa"), hookio.Ask, hookio.Ask},
		{"in-repo api-token.json basename arm", filepath.Join(root, "repo", "config", "api-token.json"), hookio.Ask, hookio.Ask},

		// Ordinary in-repo source is untouched in either direction.
		{"ordinary in-repo file", filepath.Join(root, "repo", "README.md"), hookio.NoOpinion, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for label, input := range map[string]*hookio.HookInput{
				"Bash cat": bashInput("cat " + tt.path),
				"Read":     fileInput("Read", tt.path),
			} {
				if got := hookio.Verdict(r.Evaluate(input)); got.Decision != tt.read {
					t.Errorf("%s %s = %v, want %v (reason %q)", label, tt.path, got.Decision, tt.read, got.Reason)
				}
			}
			if got := hookio.Verdict(r.Evaluate(fileInput("Write", tt.path))); got.Decision != tt.write {
				t.Errorf("Write %s = %v, want %v (reason %q)", tt.path, got.Decision, tt.write, got.Reason)
			}
		})
	}
}

// GUARD 3, stated as a RELATION rather than as the table's hardcoded pair: a WRITE
// to a path is never LESS restrictive than a READ of the same path. That is the
// property the ruling requires ("reads and writes MUST stay distinguished"), and it
// is what a table of verdicts cannot keep — a later retuning that relaxed writes to
// match reads would leave every row of the table passing.
//
// hookio.Decision is ordered by restrictiveness, so the relation is
// `write >= read`, one-directional on purpose: equal is fine (most paths), weaker
// is the failure.
func TestRule_WriteNeverLessRestrictiveThanRead(t *testing.T) {
	root := repoScopeFixture(t)
	project := t.TempDir()
	r := New(patheval.NewWithCWD(project, project))
	for _, p := range []string{
		filepath.Join(root, "repo", "internal", "rules", "secrets", "secrets.go"),
		filepath.Join(root, "repo", "deploy", "secrets", "token"),
		filepath.Join(root, "wt", "secrets", "token"),
		filepath.Join(root, "secrets", "prod.env"),
		filepath.Join(root, "repo", ".env"),
		filepath.Join(root, "repo", "secrets", ".ssh", "id_rsa"),
		filepath.Join(root, "repo", "README.md"),
	} {
		t.Run(p, func(t *testing.T) {
			read := hookio.Verdict(r.Evaluate(fileInput("Read", p)))
			write := hookio.Verdict(r.Evaluate(fileInput("Write", p)))
			if write.Decision < read.Decision {
				t.Errorf("%s: Write = %v is LESS restrictive than Read = %v", p, write.Decision, read.Decision)
			}
		})
	}
}

// The relaxation FAILS CLOSED when the repo question cannot be answered. A nil
// evaluator is a supported configuration (it makes the rule cwd-independent — see
// resolve), and with it there is no cwd to expand a path against, so the `secrets`
// arm must keep FIRING rather than silently relaxing. The relaxation only ever
// removes a prompt, so an unanswerable question must cost an Ask, never silence.
func TestRule_InRepoRelaxationFailsClosedWithoutAnEvaluator(t *testing.T) {
	root := repoScopeFixture(t)
	r := New(nil)
	for _, p := range []string{
		filepath.Join(root, "repo", "internal", "rules", "secrets", "secrets.go"),
		filepath.Join(root, "repo", "deploy", "secrets", "token"),
	} {
		if got := hookio.Verdict(r.Evaluate(fileInput("Read", p))); got.Decision != hookio.Ask {
			t.Errorf("nil evaluator, Read %s = %v, want ask — the relaxation must fail closed (reason %q)", p, got.Decision, got.Reason)
		}
	}
}

// The ACCEPTANCE CRITERION of pg2-pmk9q against the REAL path it was reported
// for — this very file — rather than a fixture that merely resembles it. The four
// observed asklog rows (327201, 327344, 327371, 327471) were all read-only
// inspection of this directory.
//
// It SKIPS rather than fails when the checkout is not a git working tree, and that
// is not a hedge: the nix build sandbox copies the source WITHOUT `.git`, so in
// that environment the premise of the test genuinely does not hold and the
// unconditional guarantee is carried by repoScopeFixture instead.
func TestRule_ReadingThisRulesOwnSourceNoLongerPrompts(t *testing.T) {
	self, err := filepath.Abs("secrets.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := secretpath.Classify(self); got != secretpath.GenericSecretsDir {
		t.Fatalf("precondition: Classify(%s) = %v, want GenericSecretsDir — this path is the reported false positive and must still match the arm being relaxed", self, got)
	}
	if !patheval.InGitRepo(self) {
		t.Skipf("checkout at %s is not a git working tree (nix build sandbox); repoScopeFixture carries this guarantee unconditionally", self)
	}
	dir := filepath.Dir(self)
	r := New(patheval.NewWithCWD(dir, dir))
	for label, input := range map[string]*hookio.HookInput{
		"Read absolute":    fileInput("Read", self),
		"Read relative":    fileInput("Read", "secrets.go"),
		"Bash cat":         bashInput("cat " + self),
		"Bash git show":    bashInput("git show HEAD:internal/rules/secrets/secrets.go"),
		"Grep in this dir": searchInput("Grep", "func Evaluate", dir),
	} {
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("%s = %v, want abstain — reading the secrets rule's own source must not prompt (reason %q)", label, got.Decision, got.Reason)
		}
	}
	// A WRITE to the same file is NOT relaxed (guard 3).
	if got := hookio.Verdict(r.Evaluate(fileInput("Write", self))); got.Decision != hookio.Ask {
		t.Errorf("Write %s = %v, want ask — the relaxation is read-only (reason %q)", self, got.Decision, got.Reason)
	}
}

// ===========================================================================
// pg2-fhb9q — the CONFIG-DRIVEN arm.
// ===========================================================================

// denyListFixture builds a credential-store fixture and points HOME at it, so the
// `~/` spellings the bead measured expand into it. It returns the fixture root and
// a rule whose evaluator deny-lists the four credential directories the bead names
// but which secretpath does NOT recognize.
//
// HOME is set BEFORE the evaluator is constructed because patheval resolves home
// once, at construction (patheval.NewWithCWD).
func denyListFixture(t *testing.T) (string, *Rule) {
	t.Helper()
	home := t.TempDir()
	for _, f := range []string{
		filepath.Join(".kube", "config"),
		filepath.Join(".docker", "config.json"),
		filepath.Join(".aws", "credentials"),
		".netrc",
		filepath.Join("notes", "README.md"),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(home, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	project := t.TempDir()
	pe := patheval.NewWithCWD(project, project)
	denied := []string{
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".netrc"),
	}
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{DenyRead: denied, DenyWrite: denied})
	return home, New(pe)
}

// THE MEASURED DEFECT, and the acceptance criterion of pg2-fhb9q: a path under a
// configured denyRead prefix must be screened WITHOUT needing a secretpath entry.
//
// Before the config arm, secretpath.IsSecret GATED whether the deny-list was
// consulted at all — decide() ran only on a reference a lexical arm had already
// matched — so `cat ~/.aws/credentials` returned {} on main despite `.aws` sitting
// in the machine's sandbox.filesystem.denyRead. The lexical list was UPSTREAM of
// the configured one, and that inversion is what this test pins closed.
//
// EVERY POSITIVE ROW HAS ITS secretpath.IsSecret == false ASSERTED as a
// precondition. Without that the rows could pass for the wrong reason — a lexical
// hit — and the test would keep passing while testing nothing.
func TestRule_ConfigDenyListScreensWithoutASecretpathEntry(t *testing.T) {
	home, r := denyListFixture(t)
	tests := []struct {
		name string
		path string
		want hookio.Decision
	}{
		// MATCHING — deny-listed, and NOT recognized by any lexical arm.
		{"kube config", filepath.Join(home, ".kube", "config"), hookio.Reject},
		{"docker registry auth", filepath.Join(home, ".docker", "config.json"), hookio.Reject},
		{"aws credentials", filepath.Join(home, ".aws", "credentials"), hookio.Reject},
		{"netrc (a deny-listed FILE, not a directory)", filepath.Join(home, ".netrc"), hookio.Reject},
		// NON-MATCHING, adjacent — under HOME but under no deny prefix, so the
		// config arm must not sweep the whole home directory in.
		{"unrelated file under home", filepath.Join(home, "notes", "README.md"), hookio.NoOpinion},
		{"sibling of a deny prefix", filepath.Join(home, ".kubeconfig-backup"), hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if secretpath.IsSecret(tt.path) {
				t.Fatalf("precondition: %s is lexically secret, so this row does not test the CONFIG arm", tt.path)
			}
			for label, input := range map[string]*hookio.HookInput{
				"Bash cat": bashInput("cat " + tt.path),
				"Read":     fileInput("Read", tt.path),
				"Grep":     searchInput("Grep", "x", tt.path),
			} {
				got := hookio.Verdict(r.Evaluate(input))
				if got.Decision != tt.want {
					t.Errorf("%s %s = %v, want %v (reason %q)", label, tt.path, got.Decision, tt.want, got.Reason)
				}
			}
		})
	}
	// The `~/` spelling the bead measured reaches the same verdict — the arm keys on
	// the path patheval expands, not on the literal the caller typed.
	if got := hookio.Verdict(r.Evaluate(bashInput("cat ~/.kube/config"))); got.Decision != hookio.Reject {
		t.Errorf("cat ~/.kube/config = %v, want reject (reason %q)", got.Decision, got.Reason)
	}
}

// Stated as a RELATION rather than as hardcoded verdicts, because the safety
// property is not "these paths Reject" — it is "CONFIGURING a deny-list can never
// make ceta LESS restrictive about a path". The inversion this bead fixed was
// exactly a case where the configured list had no effect at all, and a relation
// keeps that pinned through any later retuning of what either side decides.
//
// hookio.Decision is ordered by restrictiveness (Approve < NoOpinion < Ask <
// Reject), so "not less restrictive" is `configured >= unconfigured`.
func TestRule_ConfigDenyListIsNeverLessRestrictiveThanNoConfig(t *testing.T) {
	home, configured := denyListFixture(t)
	// Same cwd/home, no sandbox config at all.
	unconfigured := New(patheval.NewWithCWD(t.TempDir(), t.TempDir()))
	for _, p := range []string{
		filepath.Join(home, ".kube", "config"),
		filepath.Join(home, ".docker", "config.json"),
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, "notes", "README.md"),
		filepath.Join(home, ".ssh", "id_rsa"), // lexical too — must not regress either
	} {
		t.Run(p, func(t *testing.T) {
			with := hookio.Verdict(configured.Evaluate(bashInput("cat " + p)))
			without := hookio.Verdict(unconfigured.Evaluate(bashInput("cat " + p)))
			if with.Decision < without.Decision {
				t.Errorf("cat %s: configured = %v is LESS restrictive than unconfigured = %v", p, with.Decision, without.Decision)
			}
		})
	}
}

// BLAST-RADIUS BOUND on the config arm, and the reason it is gated on
// isPathShaped: IsDenyRead ABSOLUTIZES against the cwd, so testing a bare word
// would reclassify it as a file in the current directory. With the cwd inside a
// deny-listed tree, `kubectl get secrets` would resolve `secrets` to
// `<cwd>/secrets`, land inside that tree, and hard-REJECT — worse than the
// pg2-ia640.2 false-positive class it revives, because a Reject cannot be waved
// through at the prompt.
func TestRule_ConfigDenyListDoesNotResolveBareWords(t *testing.T) {
	denied := t.TempDir()
	cwd := filepath.Join(denied, "proj")
	if err := os.MkdirAll(filepath.Join(cwd, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	pe := patheval.NewWithCWD(cwd, cwd)
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{DenyRead: []string{denied}})
	r := New(pe)
	if got := hookio.Verdict(r.Evaluate(bashInput("kubectl get secrets"))); got.Decision != hookio.NoOpinion {
		t.Errorf("kubectl get secrets with a deny-listed cwd = %v, want abstain (reason %q)", got.Decision, got.Reason)
	}
	// The path-shaped spelling of a file in that same deny-listed tree IS screened,
	// so the bound costs no coverage for anything that names a path.
	if got := hookio.Verdict(r.Evaluate(bashInput("cat " + filepath.Join(cwd, "notes.txt")))); got.Decision != hookio.Reject {
		t.Errorf("cat <deny-listed>/notes.txt = %v, want reject (reason %q)", got.Decision, got.Reason)
	}
}

// A deny-listed secret reached VIA a symlink is still Reject, not Ask: patheval's
// IsDenyRead resolves symlinks itself, so the escalation survives the indirection.
func TestRule_DenyListedSecretViaSymlink_Rejects(t *testing.T) {
	root := linkFixture(t)
	project := t.TempDir()
	pe := patheval.NewWithCWD(project, project)
	resolvedSSH, err := filepath.EvalSymlinks(filepath.Join(root, ".ssh"))
	if err != nil {
		t.Fatal(err)
	}
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{DenyRead: []string{resolvedSSH}})
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(fileInput("Read", filepath.Join(root, "mykeys", "id_rsa"))))
	if got.Decision != hookio.Reject {
		t.Errorf("Read deny-listed secret via symlink = %v, want reject (reason %q)", got.Decision, got.Reason)
	}
}
