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

// TestSkipBBProseArgs pins the bb prose carve-out (tc-3bmy) at the filter level,
// analogous to TestSkipMessageArgs for bd/git/gh: which subcommand shapes carry a
// bare positional prose slot, which --set/--set-json fields are known prose, and
// which tokens must be left alone because bb has no path-taking argument to
// accidentally over-suppress.
//
// The rule-level (Ask vs Abstain) consequences are asserted in
// internal/rules/secrets/secrets_test.go.
func TestSkipBBProseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		// --- positional prose ---------------------------------------------
		{
			name: "bb note positional text dropped, id kept",
			args: []string{"note", "task-abc", "see ~/.ssh/id_rsa for details"},
			want: []string{"note", "task-abc"},
		},
		{
			name: "bb comment add positional text dropped, target-id kept",
			args: []string{"comment", "add", "task-abc", "prose naming a/secrets/x"},
			want: []string{"comment", "add", "task-abc"},
		},
		{
			name: "bb comment add with trailing flags",
			args: []string{"comment", "add", "task-abc", "prose ~/.ssh/agent", "--author", "alice"},
			want: []string{"comment", "add", "task-abc", "--author", "alice"},
		},
		{
			name: "bb add positional title dropped",
			args: []string{"add", "SECURITY note about ~/.ssh/agent"},
			want: []string{"add"},
		},
		{
			name: "bb task create positional title dropped",
			args: []string{"task", "create", "SECURITY note about ~/.ssh/agent"},
			want: []string{"task", "create"},
		},
		{
			name: "bb note target-id positional is NOT dropped",
			args: []string{"note", "~/.ssh/id_rsa", "body"},
			want: []string{"note", "~/.ssh/id_rsa"},
		},
		{
			name: "bb comment add with a flag before target-id keeps every positional",
			args: []string{"comment", "add", "--author", "a", "~/.ssh/id_rsa", "body"},
			want: []string{"comment", "add", "--author", "a", "~/.ssh/id_rsa", "body"},
		},
		{
			name: "a non-prose bb subcommand drops no positional",
			args: []string{"show", "task-abc", "secrets/prod.yaml"},
			want: []string{"show", "task-abc", "secrets/prod.yaml"},
		},
		{
			name: "bb comment (without add) is not the prose shape",
			args: []string{"comment", "task-abc", "prose ~/.ssh/agent"},
			want: []string{"comment", "task-abc", "prose ~/.ssh/agent"},
		},

		// --- --set / --set-json prose fields -------------------------------
		{
			name: "bb put --set body.description value dropped",
			args: []string{"put", "--id", "task-abc", "--set", "body.description=see ~/.ssh/agent for details"},
			want: []string{"put", "--id", "task-abc", "--set"},
		},
		{
			name: "bb put --set body.title value dropped",
			args: []string{"put", "--id", "task-abc", "--set", "body.title=SECURITY ~/.ssh/x"},
			want: []string{"put", "--id", "task-abc", "--set"},
		},
		{
			name: "bb put --set body.text value dropped (comment object)",
			args: []string{"put", "--type", "comment", "--set", "body.text=names a/secrets/y"},
			want: []string{"put", "--type", "comment", "--set"},
		},
		{
			name: "bb task update --set body.title value dropped",
			args: []string{"task", "update", "task-abc", "--set", "body.title=prose ~/.ssh/agent"},
			want: []string{"task", "update", "task-abc", "--set"},
		},
		{
			name: "bb --set-json body.description value dropped",
			args: []string{"put", "--id", "task-abc", "--set-json", "body.description=\"see ~/.ssh/agent\""},
			want: []string{"put", "--id", "task-abc", "--set-json"},
		},
		{
			name: "bb --set JSON-typed (:=) spelling value dropped",
			args: []string{"put", "--id", "task-abc", "--set", "body.title:=\"~/.ssh/agent\""},
			want: []string{"put", "--id", "task-abc", "--set"},
		},
		{
			name: "bb --set equals-glued spelling drops the whole token",
			args: []string{"put", "--id", "task-abc", "--set=body.description=see ~/.ssh/agent"},
			want: []string{"put", "--id", "task-abc"},
		},
		{
			name: "bb --set on a non-prose field keeps its value",
			args: []string{"task", "update", "task-abc", "--set", "body.owner=~/.ssh/agent"},
			want: []string{"task", "update", "task-abc", "--set", "body.owner=~/.ssh/agent"},
		},
		{
			name: "bb --set as the LAST token consumes nothing",
			args: []string{"put", "--id", "task-abc", "--set"},
			want: []string{"put", "--id", "task-abc", "--set"},
		},

		// --- boundaries ------------------------------------------------------
		{
			name: "operands after -- are never read as flags",
			args: []string{"put", "--", "--set", "body.title=~/.ssh/agent"},
			want: []string{"put", "--", "--set", "body.title=~/.ssh/agent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SkipBBProseArgs(tt.args)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("SkipBBProseArgs(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestGluedFlagValue pins the pg2-52eod centralization: GluedFlagValue returns the
// value half of a `--flag=value` token with ONE matched pair of surrounding shell
// quotes already removed, and reports malformed whenever UnwrapGluedQuotes DECLINES
// on a value that opened with a quote character (see UnwrapGluedQuotes' own
// TestUnwrapGluedQuotes for the exact fixture-construction technique this table
// follows).
func TestGluedFlagValue(t *testing.T) {
	tests := []struct {
		name          string
		arg           string
		wantValue     string
		wantOK        bool
		wantMalformed bool
	}{
		// NOT a glued form at all.
		{"bare positional, no flag prefix", "value", "", false, false},
		{"bare flag, no value", "--flag", "", false, false},
		{"empty glued value", "--flag=", "", false, false},
		{"bare dash", "-", "", false, false},
		{"bare double-dash", "--", "", false, false},

		// CLEAN, UNQUOTED value: unchanged, matching the pre-existing contract.
		{"long flag, unquoted value", "--output=/etc/shadow", "/etc/shadow", true, false},
		{"short flag, unquoted value", "-o=/etc/shadow", "/etc/shadow", true, false},
		{"value itself contains an =", "--output=a=b", "a=b", true, false},

		// CLEAN, QUOTED value: this bead's fix. UnwrapGluedQuotes strips the ONE
		// matched pair, matching the unquoted spelling exactly.
		{"long flag, single-quoted value", "--output='/etc/shadow'", "/etc/shadow", true, false},
		{"long flag, double-quoted value", `--output="/etc/shadow"`, "/etc/shadow", true, false},
		{"basename secret, single-quoted", "--file='.env'", ".env", true, false},

		// MALFORMED: UnwrapGluedQuotes declines (see its own doc/tests for why),
		// and this primitive now reports that explicitly rather than silently
		// handing back a still quote-wrapped value with no signal attached.
		{
			"interior contains the wrapper character (multi-segment concatenation)",
			"--file='.env'x'.env'", "'.env'x'.env'", true, true,
		},
		{
			"double-wrapped: outer pair around an already-quoted inner value",
			"--file=''.env''", "''.env''", true, true,
		},
		{
			"mismatched quote characters at the two ends",
			`--file='.env"`, `'.env"`, true, true,
		},

		// pg2-su2eh: NOT a `--flag=value` token AT ALL, even though it contains an
		// "=". These are short flags glued to a QUOTED value with no "="
		// convention of their own (awk's `-F`, git log's `-S`); the value simply
		// happens to contain a literal "=" inside its quoting. strings.Cut finds
		// the FIRST "=" in the whole token, which lands INSIDE that quoted region
		// — the byte immediately before it (the opening quote) shows up in the
		// NAME half, which is the tell that the split is spurious. Before this
		// fix these fell into the MALFORMED branch above (the fragment after the
		// spurious "=" starts with a stray, unresolvable quote), producing an
		// incorrect "cannot classify" signal on a token that was never a glued
		// flag value to begin with — measured on main @07b9600b via a 360,523-row
		// corpus replay: `awk -F"=" ...` (no `.git` reference) wrongly Rejected
		// in internal/rules/gitdir, and `git log -S'plan_count{query_name="x"}'`
		// wrongly Asked in internal/rules/secrets.
		{
			`awk field separator, double-quoted, value is "="`,
			`-F"="`, "", false, false,
		},
		{
			`git log pickaxe search string containing a literal "=" inside single quotes`,
			`-S'plan_count{query_name="x"}'`, "", false, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok, malformed := GluedFlagValue(tt.arg)
			if value != tt.wantValue || ok != tt.wantOK || malformed != tt.wantMalformed {
				t.Errorf("GluedFlagValue(%q) = (%q, %v, %v), want (%q, %v, %v)",
					tt.arg, value, ok, malformed, tt.wantValue, tt.wantOK, tt.wantMalformed)
			}
		})
	}
}

// TestSkipGrepPattern_GluedQuoteParity is pg2-52eod's fix for the audited THIRD (in
// this repo, actually fourth — see internal/rules/gitdir) caller of GluedFlagValue:
// a grep/rg file-flag's value glued AND quoted (`--file='X'`) must produce the SAME
// emitted candidate as the unquoted glued spelling (`--file=X`), and malformed
// quoting must be reported rather than silently emitted as an inert, still-quoted
// candidate.
func TestSkipGrepPattern_GluedQuoteParity(t *testing.T) {
	tests := []struct {
		name          string
		cmd           string
		args          []string // args AFTER "grep"/"rg"
		wantFiles     []string
		wantMalformed bool
	}{
		{
			"grep --file, unquoted glued",
			"grep",
			[]string{"--file=/etc/shadow", "x.log"},
			[]string{"/etc/shadow", "x.log"},
			false,
		},
		{
			"grep --file, quoted glued — must match the unquoted spelling",
			"grep",
			[]string{"--file='/etc/shadow'", "x.log"},
			[]string{"/etc/shadow", "x.log"},
			false,
		},
		{
			"grep --file, malformed glued quoting: double-wrapped",
			"grep",
			[]string{"--file=''.env''", "x.log"},
			nil, true,
		},
		{
			"grep --file, malformed glued quoting: interior wrapper character",
			"grep",
			[]string{"--file='.env'x'.env'", "x.log"},
			nil, true,
		},
		{
			"rg --pre is a program flag, not this test's concern, but its value is still glued-quote-parity checked",
			"rg",
			[]string{"--hostname-bin='/etc/shadow'"},
			[]string{"/etc/shadow"},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, malformed := SkipGrepPattern(tt.cmd, tt.args)
			if malformed != tt.wantMalformed {
				t.Errorf("SkipGrepPattern(%q, %v) malformed = %v, want %v", tt.cmd, tt.args, malformed, tt.wantMalformed)
			}
			if !tt.wantMalformed && !stringSlicesEqual(files, tt.wantFiles) {
				t.Errorf("SkipGrepPattern(%q, %v) files = %v, want %v", tt.cmd, tt.args, files, tt.wantFiles)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
