package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

func TestAwkDialect(t *testing.T) {
	read := func(p, src string) Effect {
		return Effect{Kind: EffectPath, Path: p, Access: AccessRead, Source: src}
	}
	truncate := func(p, src string) Effect {
		return Effect{Kind: EffectPath, Path: p, Access: AccessTruncate, Source: src}
	}
	modify := func(p, src string) Effect {
		return Effect{Kind: EffectPath, Path: p, Access: AccessModify, Source: src}
	}
	shellChild := func(prog string) ChildInvocation {
		return ChildInvocation{Dialect: "shell", Program: prog}
	}
	cases := []struct {
		name       string
		script     string
		sufficient bool
		effects    []Effect
		children   []ChildInvocation
		reason     string // substring of Insufficiency when !sufficient
	}{
		// Inert programs: assignments, patterns, control flow, field refs,
		// user functions, comments, regex constants (including division vs
		// regex disambiguation).
		{"bare print", "{print}", true, nil, nil, ""},
		{"print field", "{print $1}", true, nil, nil, ""},
		{"pattern comparison", "$1 > 5 {print}", true, nil, nil, ""},
		{"division", "{print $1/2}", true, nil, nil, ""},
		{"regex match", "$0 ~ /foo/ {print}", true, nil, nil, ""},
		{"regex escaped slash", `$0 ~ /a\/b/ {print}`, true, nil, nil, ""},
		{"regex bracket with slash", `$0 ~ /[/]/ {print}`, true, nil, nil, ""},
		{"regex after paren", `(/foo/) {print}`, true, nil, nil, ""},
		{"comment", "# comment\n{print}", true, nil, nil, ""},
		{"assignment and function", "function f(x){return x+1} BEGIN{a=f(1)}", true, nil, nil, ""},
		{"while getline no redir", "BEGIN{while ((getline line) > 0) print line}", true, nil, nil, ""},
		{"close bare", "END{close(x)}", true, nil, nil, ""},
		{"fflush no args", "{fflush()}", true, nil, nil, ""},

		// print/printf redirection: literal target truncates or appends;
		// "/dev/stdout"/"/dev/stderr"/"-" are inert stdio.
		{"print truncate", `{print > "out.txt"}`, true, []Effect{truncate("out.txt", "awk print >")}, nil, ""},
		{"printf truncate", `{printf "%d", 1 > "out.txt"}`, true, []Effect{truncate("out.txt", "awk print >")}, nil, ""},
		{"print append", `{print >> "out.txt"}`, true, []Effect{modify("out.txt", "awk print >>")}, nil, ""},
		{"print stdout literal", `{print > "/dev/stdout"}`, true, nil, nil, ""},
		{"print stderr literal", `{print > "/dev/stderr"}`, true, nil, nil, ""},
		{"print dash literal", `{print > "-"}`, true, nil, nil, ""},
		{"print redirect nonliteral", "{print > x}", false, nil, nil, "not a string literal"},
		{"print append nonliteral field", "{print $1 >> $2}", false, nil, nil, "not a string literal"},
		{"print paren comparison not redirect", "{print (a > b)}", true, nil, nil, ""},
		{"print paren then redirect", `{print (a, b) > "out.txt"}`, true, []Effect{truncate("out.txt", "awk print >")}, nil, ""},

		// print pipe to a command: literal is a shell child; non-literal or
		// the two-way coprocess pipe is insufficient.
		{"print pipe literal", `{print | "sort"}`, true, nil, []ChildInvocation{shellChild("sort")}, ""},
		{"print pipe field nonliteral", "{print | $1}", false, nil, nil, "not a string literal"},
		{"print two way pipe", `{print |& "cmd"}`, false, nil, nil, "two-way pipe is not modeled"},
		{"cmd two way pipe getline", `BEGIN{"cmd" |& getline}`, false, nil, nil, "two-way pipe is not modeled"},

		// system(): a single string literal is a shell child; anything else
		// (variable, field, concatenation) is insufficient.
		{"system literal", `BEGIN{system("cat README.md")}`, true, nil, []ChildInvocation{shellChild("cat README.md")}, ""},
		{"system field nonliteral", "{system($1)}", false, nil, nil, "not a literal"},
		{"system concat nonliteral", `{system("cat " f)}`, false, nil, nil, "not a single string literal"},

		// getline: bare and `< "file"` forms; the pipe form is
		// topLevelPipe's job (tested separately below).
		{"getline bare", "{getline}", true, nil, nil, ""},
		{"getline var", "{getline line}", true, nil, nil, ""},
		{"getline redirect literal", `{getline < "in.txt"}`, true, []Effect{read("in.txt", "awk getline <")}, nil, ""},
		{"getline var redirect literal", `{getline line < "in.txt"}`, true, []Effect{read("in.txt", "awk getline <")}, nil, ""},
		{"getline redirect nonliteral", "{getline < f}", false, nil, nil, "not a string literal"},

		// "cmd" | getline: a literal command immediately before the pipe is
		// a shell child; a non-literal (even a value that WAS a string two
		// tokens back) is insufficient.
		{"cmd pipe getline", `BEGIN{"date" | getline d}`, true, nil, []ChildInvocation{shellChild("date")}, ""},
		{"cmd pipe getline no var", `BEGIN{"date" | getline}`, true, nil, []ChildInvocation{shellChild("date")}, ""},
		{"cmd pipe getline nonliteral", "BEGIN{cmd | getline d}", false, nil, nil, "not a string literal"},
		{"stale literal not reused", `BEGIN{x = "date"; cmd | getline}`, false, nil, nil, "not a string literal"},
		{"unrecognised pipe", "BEGIN{x = 1 | 2}", false, nil, nil, "unrecognised `|`"},

		// @include / @load.
		{"at include literal", `@include "ext.awk"`, true, []Effect{read("ext.awk", "awk @include")}, nil, ""},
		{"at include nonliteral", "@include f", false, nil, nil, "not a string literal"},
		{"at load", `@load "fnmatch"`, false, nil, nil, "not modeled"},
		{"at unknown directive", "@frobnicate", false, nil, nil, "unrecognised @"},

		// Malformed/ambiguous: must be insufficient.
		{"unterminated string", `{print "x}`, false, nil, nil, "unterminated string"},
		{"unterminated regex", "$0 ~ /x", false, nil, nil, "unterminated regex"},
		{"unterminated print", `{print "x" >`, false, nil, nil, "not a string literal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := awkDialect{}.InterpretProgram(tc.script, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%q), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient && (got.Insufficiency == "" || !strings.Contains(got.Insufficiency, tc.reason)) {
				t.Errorf("insufficiency = %q, want substring %q", got.Insufficiency, tc.reason)
			}
			if !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
			if !reflect.DeepEqual(got.Children, tc.children) {
				t.Errorf("children = %+v\nwant     %+v", got.Children, tc.children)
			}
		})
	}
}

// TestAwkDialectRegistered mirrors TestUnknownDialectFailsClosed's own
// registration check: "awk" must be a resolvable dialect.
func TestAwkDialectRegistered(t *testing.T) {
	if _, ok := LookupDialect("awk"); !ok {
		t.Error("awk dialect must be registered")
	}
}
