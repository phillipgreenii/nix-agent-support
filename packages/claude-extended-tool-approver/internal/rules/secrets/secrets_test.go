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
		// grep -e/-f pattern-source values, rg value-flag values, and the jq value
		// flags + bare filter are NOT secret file paths — must Abstain, not Ask.
		{"grep pattern .env is not a file", bashInput("grep .env file.log"), hookio.NoOpinion},
		{"rg pattern .env is not a file", bashInput("rg .env somefile.log"), hookio.NoOpinion},
		{"grep -e .env pattern value is not a file", bashInput("grep -e .env file.log"), hookio.NoOpinion},
		{"grep -f .env pattern-file value is not a file", bashInput("grep -f .env file.log"), hookio.NoOpinion},
		{"rg -g glob value is not a file", bashInput("rg -g '*.env' pattern file.log"), hookio.NoOpinion},
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
