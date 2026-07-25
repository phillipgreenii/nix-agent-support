package secrets

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
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
		{"grep pattern .env is not a file", bashInput("grep .env file.log"), hookio.Abstain},
		{"rg pattern .env is not a file", bashInput("rg .env somefile.log"), hookio.Abstain},
		{"grep -e .env pattern value is not a file", bashInput("grep -e .env file.log"), hookio.Abstain},
		{"grep -f .env pattern-file value is not a file", bashInput("grep -f .env file.log"), hookio.Abstain},
		{"rg -g glob value is not a file", bashInput("rg -g '*.env' pattern file.log"), hookio.Abstain},
		{"jq --arg value .env is not a file", bashInput("jq --arg x .env '.'"), hookio.Abstain},
		{"jq bare filter .credentials is not a file", bashInput("jq '.credentials' data.json"), hookio.Abstain},

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
		{"bash --rcfile not a wrapper", bashInput("bash --rcfile ~/.bashrc"), hookio.Abstain},
		// A combined-flag wrapper whose inner command reads no secret must not
		// over-fire.
		{"bash -lc echo hi", bashInput("bash -lc 'echo hi'"), hookio.Abstain},

		// Bash without a secret path → Abstain (defer to rest of chain)
		{"cat readme", bashInput("cat README.md"), hookio.Abstain},
		{"echo hello", bashInput("echo hello"), hookio.Abstain},
		{"ls tmp", bashInput("ls /tmp"), hookio.Abstain},
		// bare "secrets" word (kubectl subcommand) must NOT be flagged
		{"kubectl get secrets not flagged", bashInput("kubectl get secrets"), hookio.Abstain},

		// File tools
		{"Read credentials", fileInput("Read", "~/.claude/.credentials"), hookio.Ask},
		{"Read normal file", fileInput("Read", "internal/main.go"), hookio.Abstain},
		{"Write to secret", fileInput("Write", "~/.ssh/authorized_keys"), hookio.Ask},
		{"Edit normal file", fileInput("Edit", "src/app.go"), hookio.Abstain},

		// Search tools
		{"Grep in secrets dir", searchInput("Grep", "password", "secrets/"), hookio.Ask},
		{"Grep normal dir", searchInput("Grep", "TODO", "internal/"), hookio.Abstain},
		{"Glob no path", searchInput("Glob", "**/*.go", ""), hookio.Abstain},

		// Unrelated tools → Abstain
		{"WebFetch", &hookio.HookInput{ToolName: "WebFetch"}, hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Evaluate(tt.input)
			if got.Decision != tt.want {
				t.Errorf("Evaluate(%s) decision = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
			if got.Decision != hookio.Abstain && got.Module != r.Name() {
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
			if got := r.Evaluate(tt.input); got.Decision != tt.want {
				t.Errorf("Evaluate(%s) = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
