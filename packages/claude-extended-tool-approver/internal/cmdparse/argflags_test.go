package cmdparse

import (
	"strings"
	"testing"
)

// TestSkipMessageArgs pins the ENUMERATED boundary of the message carve-out
// (pg2-ia640.5) at the level of the filter itself, so each half of the boundary
// has a named case: which executables it applies to, which flags it consumes a
// value for, which spellings of a flag it recognizes, and which tokens it must
// leave alone because they name FILES.
//
// The rule-level consequences (Ask vs Abstain) are asserted in
// internal/rules/secrets/secrets_test.go; here only the arg list is compared, so
// a failure names the filter rather than a decision.
func TestSkipMessageArgs(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		want []string
	}{
		// --- the closed set of executables -----------------------------------
		{
			name: "unlisted executable is untouched",
			cmd:  "cp", args: []string{"~/.ssh/id_rsa", "/tmp"},
			want: []string{"~/.ssh/id_rsa", "/tmp"},
		},
		{
			name: "unlisted executable keeps a message-looking flag value",
			cmd:  "notes-tool", args: []string{"--reason", "~/.ssh/agent"},
			want: []string{"--reason", "~/.ssh/agent"},
		},

		// --- bd: flag-valued -------------------------------------------------
		{
			name: "bd close --reason value dropped",
			cmd:  "bd", args: []string{"close", "pg2-x", "--reason", "see ~/.ssh/agent", "--actor", "a"},
			want: []string{"close", "pg2-x", "--actor", "a"},
		},
		{
			name: "bd create --title and --description values dropped",
			cmd:  "bd", args: []string{"create", "--title", "SECURITY ~/.ssh/x", "--description", "names a/secrets/y"},
			want: []string{"create"},
		},
		{
			name: "bd update --append-notes value dropped",
			cmd:  "bd", args: []string{"update", "pg2-x", "--append-notes", "cert via ~/.ssh/agent"},
			want: []string{"update", "pg2-x"},
		},
		{
			name: "bd equals spelling drops the whole token",
			cmd:  "bd", args: []string{"close", "pg2-x", "--reason=see ~/.ssh/agent"},
			want: []string{"close", "pg2-x"},
		},
		{
			name: "bd file-taking flags are NOT dropped",
			cmd:  "bd", args: []string{"comment", "x", "--file", "secrets/notes.txt", "--body-file", "secrets/b", "--design-file", "secrets/d"},
			want: []string{"comment", "x", "--file", "secrets/notes.txt", "--body-file", "secrets/b", "--design-file", "secrets/d"},
		},
		{
			name: "bd message flag as the LAST token consumes nothing",
			cmd:  "bd", args: []string{"close", "pg2-x", "--reason"},
			want: []string{"close", "pg2-x", "--reason"},
		},

		// --- bd: the comment body positional ---------------------------------
		{
			name: "bd comment body positional dropped, id kept",
			cmd:  "bd", args: []string{"comment", "pg2-14vjq", "prose naming a/secrets/x"},
			want: []string{"comment", "pg2-14vjq"},
		},
		{
			name: "bd comment body dropped with trailing flags",
			cmd:  "bd", args: []string{"comment", "pg2-olt3", "prose ~/.ssh/agent", "--actor", "a"},
			want: []string{"comment", "pg2-olt3", "--actor", "a"},
		},
		{
			name: "bd comment id positional is NOT dropped",
			cmd:  "bd", args: []string{"comment", "~/.ssh/id_rsa", "body"},
			want: []string{"comment", "~/.ssh/id_rsa"},
		},
		{
			name: "bd comment with a flag before the id keeps every positional",
			cmd:  "bd", args: []string{"comment", "--actor", "a", "~/.ssh/id_rsa", "body"},
			want: []string{"comment", "--actor", "a", "~/.ssh/id_rsa", "body"},
		},
		{
			name: "bd comment second body word is kept (only one token is dropped)",
			cmd:  "bd", args: []string{"comment", "x", "see", "secrets/prod.yaml"},
			want: []string{"comment", "x", "secrets/prod.yaml"},
		},
		{
			name: "a non-comment bd subcommand drops no positional",
			cmd:  "bd", args: []string{"show", "x", "secrets/prod.yaml"},
			want: []string{"show", "x", "secrets/prod.yaml"},
		},

		// --- git -------------------------------------------------------------
		{
			name: "git commit -m value dropped",
			cmd:  "git", args: []string{"commit", "-m", "note about a/secrets/prod.yaml"},
			want: []string{"commit"},
		},
		{
			name: "git commit --message value dropped",
			cmd:  "git", args: []string{"commit", "--message", "see ~/.ssh/agent"},
			want: []string{"commit"},
		},
		{
			name: "git -C dir commit -m still resolves the subcommand",
			cmd:  "git", args: []string{"-C", "/repo", "commit", "-m", "see ~/.ssh/agent"},
			want: []string{"-C", "/repo", "commit"},
		},
		{
			name: "git commit -F path is NOT dropped",
			cmd:  "git", args: []string{"commit", "-F", "~/.ssh/id_rsa"},
			want: []string{"commit", "-F", "~/.ssh/id_rsa"},
		},
		{
			name: "git commit --file path is NOT dropped",
			cmd:  "git", args: []string{"commit", "--file", "~/.ssh/id_rsa"},
			want: []string{"commit", "--file", "~/.ssh/id_rsa"},
		},
		{
			name: "git checkout -m is boolean, so its pathspec is NOT dropped",
			cmd:  "git", args: []string{"checkout", "-m", "~/.ssh/config"},
			want: []string{"checkout", "-m", "~/.ssh/config"},
		},
		{
			name: "git operands after -- are never read as flags",
			cmd:  "git", args: []string{"commit", "--", "-m", "~/.ssh/id_rsa"},
			want: []string{"commit", "--", "-m", "~/.ssh/id_rsa"},
		},

		// --- gh --------------------------------------------------------------
		{
			name: "gh pr comment --body value dropped",
			cmd:  "gh", args: []string{"pr", "comment", "1", "--body", "see ~/.ssh/agent"},
			want: []string{"pr", "comment", "1"},
		},
		{
			name: "gh issue create -t and -b values dropped",
			cmd:  "gh", args: []string{"issue", "create", "-t", "~/.ssh/agent", "-b", "a/secrets/x"},
			want: []string{"issue", "create"},
		},
		{
			name: "gh --body-file path is NOT dropped",
			cmd:  "gh", args: []string{"pr", "create", "--body-file", "~/.ssh/id_rsa"},
			want: []string{"pr", "create", "--body-file", "~/.ssh/id_rsa"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SkipMessageArgs(tt.cmd, tt.args)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("SkipMessageArgs(%q, %q) = %q, want %q", tt.cmd, tt.args, got, tt.want)
			}
		})
	}
}

// TestSkipMessageArgs_NeverDropsMoreThanItKeepsFileFlags states the invariant as a
// RELATION rather than a verdict list: for every listed executable, the filter
// MUST return an arg list that still contains every value of a FILE-taking flag.
// Written this way it keeps holding when the message tables are extended — which
// is the moment the mistake would be made.
func TestSkipMessageArgs_FileFlagValuesAlwaysSurvive(t *testing.T) {
	const secret = "~/.ssh/id_rsa"
	cases := []struct {
		cmd  string
		args []string
	}{
		{"git", []string{"commit", "-F", secret}},
		{"git", []string{"commit", "--file", secret}},
		{"git", []string{"tag", "-a", "v1", "-F", secret}},
		{"gh", []string{"pr", "create", "-F", secret}},
		{"gh", []string{"pr", "create", "--body-file", secret}},
		{"bd", []string{"comment", "x", "--file", secret}},
		{"bd", []string{"create", "--body-file", secret}},
		{"bd", []string{"create", "--design-file", secret}},
		{"bd", []string{"create", "-f", secret}},
		{"bd", []string{"create", "--graph", secret}},
	}
	for _, c := range cases {
		kept := false
		for _, a := range SkipMessageArgs(c.cmd, c.args) {
			if a == secret {
				kept = true
			}
		}
		if !kept {
			t.Errorf("SkipMessageArgs(%q, %q) dropped a FILE-flag value; the command OPENS that path, so it must stay a candidate", c.cmd, c.args)
		}
	}
}
