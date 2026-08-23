package cmdparse

import (
	"reflect"
	"testing"
)

// TestInCommandVars_EstablishedBindings covers WHICH leaves establish a variable for the
// rest of the expression. Every negative row is a shape bash would NOT have expanded to
// the value written in it, so resolving it would be a WRONG answer rather than merely a
// permissive one — which is why they are here rather than treated as missed opportunities.
func TestInCommandVars_EstablishedBindings(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{
			name: "bare assignment before a command",
			cmd:  `WT=/abs/worktree && git -C "$WT" commit`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "';' separator",
			cmd:  `WT=/abs/worktree; git status`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "double-quoted value",
			cmd:  `WT="/abs/work tree" && git status`,
			want: map[string]string{"WT": "/abs/work tree"},
		},
		{
			name: "single-quoted value",
			cmd:  `WT='/abs/work tree' && git status`,
			want: map[string]string{"WT": "/abs/work tree"},
		},
		{
			name: "relative value is still a literal",
			cmd:  `SUB=sub/dir && git status`,
			want: map[string]string{"SUB": "sub/dir"},
		},
		{
			name: "empty value binds the empty string",
			cmd:  `WT= && git status`,
			want: map[string]string{"WT": ""},
		},
		{
			name: "export establishes it",
			cmd:  `export WT=/abs/worktree && git status`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "several assignments on one leaf",
			cmd:  `A=/a B=/b` + "\n" + `git status`,
			want: map[string]string{"A": "/a", "B": "/b"},
		},
		{
			name: "the last assignment to a name wins",
			cmd:  `WT=/first && WT=/second && git status`,
			want: map[string]string{"WT": "/second"},
		},
		{
			// A PREFIX assignment is scoped to that one command's environment, and bash
			// expands the command's own words BEFORE applying it.
			name: "prefix assignment on a command leaf establishes nothing",
			cmd:  `WT=/abs/worktree git status && git status`,
			want: nil,
		},
		{
			name: "env-prefix form establishes nothing",
			cmd:  `env WT=/abs/worktree git status && git status`,
			want: nil,
		},
		{
			// A pipeline stage runs in a subshell.
			name: "assignment in a pipeline stage establishes nothing",
			cmd:  `WT=/abs/worktree | cat && git status`,
			want: nil,
		},
		{
			// pg2-ft2hl. `declare` and `typeset` are the SAME builtin and the unflagged
			// form is a plain shell-variable assignment, so it must resolve exactly like
			// the bare spelling. The lowering keeps the assignment in Args rather than
			// EnvVars, and declWrites reads it there — see incommandvars.go's SCOPE.
			// TestInCommandVars_AssignmentBuiltins covers the forms this one MUST NOT read.
			name: "declare establishes it",
			cmd:  `declare WT=/abs/worktree && git status`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "typeset establishes it",
			cmd:  `typeset WT=/abs/worktree && git status`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			// DECLINED by operator ruling, reason recorded in assignmentBuiltinReads.
			name: "local establishes nothing",
			cmd:  `local WT=/abs/worktree && git status`,
			want: nil,
		},
		{
			name: "readonly establishes nothing",
			cmd:  `readonly WT=/abs/worktree && git status`,
			want: nil,
		},
		{
			// bash's ARRAY form binds a LIST and `$arr` is its FIRST ELEMENT, so reading
			// the parenthesised text would be a WRONG value. Verified against bash 5.3.9:
			// `arr=(/a /b); echo "$arr"` prints `/a`.
			name: "array form is not a scalar literal",
			cmd:  `arr=(/a /b) && git status`,
			want: nil,
		},
		{
			// …but a QUOTED paren really is the scalar it looks like:
			// `WT="(/a /b)"; echo "$WT"` prints `(/a /b)`.
			name: "a quoted paren is a scalar literal",
			cmd:  `WT="(/a /b)" && git status`,
			want: map[string]string{"WT": "(/a /b)"},
		},
		{
			name: "command substitution value is not derived",
			cmd:  `WT=$(git rev-parse --show-toplevel) && git status`,
			want: nil,
		},
		{
			name: "backtick value is not derived",
			cmd:  "WT=`pwd` && git status",
			want: nil,
		},
		{
			name: "variable-reference value is not derived",
			cmd:  `WT=$HOME/x && git status`,
			want: nil,
		},
		{
			name: "single-quoted dollar is literal text but not a usable path",
			cmd:  `WT='$HOME/x' && git status`,
			want: nil,
		},
		{
			name: "glob value is not a literal path",
			cmd:  `WT=/abs/work* && git status`,
			want: nil,
		},
		{
			name: "tilde value is not a literal path",
			cmd:  `WT=~/repo && git status`,
			want: nil,
		},
		{
			name: "mixed quoting is not derivable by stripping",
			cmd:  `WT="/abs"/worktree && git status`,
			want: nil,
		},
		{
			// The append form binds a CONCATENATION with a value this seam may never
			// have seen, so it must revoke rather than bind its own half.
			name: "append form revokes an earlier literal",
			cmd:  `WT=/abs && WT+=/worktree && git status`,
			want: nil,
		},
		{
			name: "a later non-literal assignment revokes an earlier literal",
			cmd:  `WT=/abs/worktree && WT=$(mktemp -d) && git status`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.cmd)
			// `before` is the LAST leaf's index: every assignment in the row precedes it.
			got := InCommandVars(leaves, len(leaves)-1)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InCommandVars(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestInCommandVars_AssignmentBuiltins is pg2-ft2hl's table: WHICH assignment-builtin
// spellings this seam reads, and — the half that matters more — which of them REVOKE a
// binding an earlier leaf established rather than being skipped.
//
// Every "revoked" row is a shape where the value bash ends up holding is NOT the text
// written down, so keeping the earlier literal would be a CONFIDENTLY WRONG answer, not
// merely a missing one. Before this bead a `declare` leaf was skipped outright, so
// `WT=/first && declare -i WT=5+5 && …` kept `/first` while bash had made it `10`.
//
// The bash behaviours asserted here were measured against bash 5.3.9 on 2026-08-13 and
// each is quoted beside the row it justifies.
func TestInCommandVars_AssignmentBuiltins(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		// ── READ: the unflagged declare/typeset assignment ──────────────────────────
		{
			name: "several names on one declare",
			cmd:  `declare A=/a B=/b && git status`,
			want: map[string]string{"A": "/a", "B": "/b"},
		},
		{
			// `declare "WT=/a b"` sets WT to `/a b`; the lowering unquotes the argument,
			// so the value arrives already stripped and is genuinely literal.
			name: "a fully quoted declare argument",
			cmd:  `declare "WT=/a b" && git status`,
			want: map[string]string{"WT": "/a b"},
		},
		{
			// An UNFLAGGED naked name is a NO-OP for the value:
			// `WT=/first; declare WT; echo "$WT"` prints `/first`.
			name: "a naked declare keeps an earlier binding",
			cmd:  `WT=/first && declare WT && git status`,
			want: map[string]string{"WT": "/first"},
		},
		{
			// A DIFFERENT name's write leaves this one alone.
			name: "declare of another name keeps this binding",
			cmd:  `WT=/first && declare OTHER=/other && git status`,
			want: map[string]string{"WT": "/first", "OTHER": "/other"},
		},
		// ── NOT READ, and REVOKING: the flagged forms ───────────────────────────────
		{
			// `declare -i N=5+5; echo "$N"` prints `10` — the value is ARITHMETIC.
			name: "-i makes the value an arithmetic evaluation",
			cmd:  `WT=/first && declare -i WT=5+5 && git status`,
			want: nil,
		},
		{
			// `declare -l L=ABC; echo "$L"` prints `abc` — the value is case-folded.
			name: "-l case-folds the value",
			cmd:  `WT=/FIRST && declare -l WT=/ABC && git status`,
			want: nil,
		},
		{
			// `t=/real; declare -n ref=t; echo "$ref"` prints `/real` — the name is an
			// ALIAS, so the value is another variable's, never the text.
			name: "-n makes the name an alias",
			cmd:  `ref=/first && declare -n ref=target && git status`,
			want: nil,
		},
		{
			// `WT=/first; declare -u WT; echo "$WT"` still prints `/first`, but the NEXT
			// assignment case-folds — so a flagged naked name must revoke too.
			name: "a flagged naked name revokes",
			cmd:  `WT=/first && declare -u WT && git status`,
			want: nil,
		},
		{
			// `-r` is `readonly` under another spelling, so it gets readonly's refusal.
			name: "-r is readonly",
			cmd:  `declare -r WT=/abs/worktree && git status`,
			want: nil,
		},
		{
			// `declare -a arr=(/a /b); echo "$arr"` prints `/a`.
			name: "an array declaration is not its text",
			cmd:  `arr=/first && declare -a arr=(/a /b) && git status`,
			want: nil,
		},
		{
			// `--` ends the option list and is refused with every other flag: the
			// allowlist this seam deliberately does not keep would have to include it.
			name: "-- is refused like any other flag",
			cmd:  `declare -- WT=/abs/worktree && git status`,
			want: nil,
		},
		{
			// A flag-only declare (`declare -f`, `declare -p WT`, `declare -A m`) writes
			// no value, so it revokes only the names it MENTIONS.
			name: "a flag-only declare of another name keeps this binding",
			cmd:  `WT=/first && declare -A m && git status`,
			want: map[string]string{"WT": "/first"},
		},
		// ── NOT READ, and REVOKING: the declined builtins ───────────────────────────
		{
			// `local` outside a function is a bash ERROR; inside one the binding dies with
			// the function. Either way an earlier literal must not survive it.
			name: "local revokes an earlier binding",
			cmd:  `WT=/first && local WT=/second && git status`,
			want: nil,
		},
		{
			// `readonly WT=/x; WT=/y` prints "readonly variable" and fails, so the revoke
			// rule this file states does not hold for it unchanged.
			name: "readonly revokes an earlier binding",
			cmd:  `WT=/first && readonly WT=/second && git status`,
			want: nil,
		},
		{
			name: "nameref revokes an earlier binding",
			cmd:  `r=/first && nameref r=t && git status`,
			want: nil,
		},
		// ── NOT READ: an array-element write, whose name is not an identifier ───────
		{
			name: "an array-element write revokes its own name only",
			cmd:  `WT=/first && declare m[a]=1 && git status`,
			want: map[string]string{"WT": "/first"},
		},
		// ── NOT READ: a PREFIX assignment makes the builtin's own write ephemeral ───
		{
			// `WT=/first; WT=/x declare WT=/y; echo "$WT"` prints `/first` — the whole
			// assignment is discarded with the temporary environment. With a DIFFERENT
			// prefix name it persists (`OTHER=/x declare WT=/y` leaves WT as `/y`), and
			// this seam does not model which, so it reads neither.
			name: "a prefix assignment on declare is not read",
			cmd:  `WT=/x declare WT=/y && git status`,
			want: nil,
		},
		{
			name: "a prefix assignment of another name is still not read",
			cmd:  `OTHER=/x declare WT=/y && git status`,
			want: nil,
		},
		{
			// `export` is a POSIX SPECIAL builtin, so an assignment before it PERSISTS:
			// `WT=/first; WT=/x export WT=/y; echo "$WT"` prints `/y`. Its own argument is
			// the later write and wins, which is what the pre-existing lift already did.
			name: "export is unaffected by the prefix case",
			cmd:  `WT=/x export WT=/y && git status`,
			want: map[string]string{"WT": "/y"},
		},
		// ── SCOPE: a pipeline stage still writes nothing, whatever the builtin ──────
		{
			name: "declare in a pipeline stage establishes nothing",
			cmd:  `declare WT=/abs/worktree | cat && git status`,
			want: nil,
		},
		{
			// The pipeline stage's write dies with its subshell, so it cannot revoke
			// either: bash leaves the earlier binding exactly as it was.
			name: "declare in a pipeline stage revokes nothing",
			cmd:  `WT=/first && { declare WT=/second | cat; } && git status`,
			want: map[string]string{"WT": "/first"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.cmd)
			got := InCommandVars(leaves, len(leaves)-1)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InCommandVars(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestInCommandVars_AssignmentBuiltinSpellingParity states pg2-ft2hl's acceptance as a
// RELATION over spellings rather than as a table of verdicts, so it survives any later
// retuning of WHICH values count as literal.
//
// For every value shape:
//
//   - `declare X=v` and `typeset X=v` resolve IDENTICALLY to the plain `X=v` — that is
//     the relief this bead authorizes, and identity (not merely "at least as much") is
//     what makes the two spellings interchangeable at a hook boundary.
//   - every DECLINED or FLAGGED spelling resolves to a SUBSET of the plain spelling's
//     bindings — never a binding the plain form does not have, and never a DIFFERENT
//     value for a name they share. That is the "no spelling becomes more permissive"
//     acceptance criterion, stated so that a future implementation of `local` or
//     `readonly` strengthens the relation instead of breaking the test.
func TestInCommandVars_AssignmentBuiltinSpellingParity(t *testing.T) {
	values := []string{
		"/abs/worktree",    // the literal that resolves
		"sub/dir",          // a relative literal
		`"/abs/work tree"`, // a quoted literal
		"$(mktemp -d)",     // a substitution: never derived
		"$HOME/x",          // a variable reference: never derived
		"~/repo",           // a tilde: never a literal path
		"/abs/work*",       // a glob
		"(/a /b)",          // bash's array form
		"",                 // the empty value
	}
	resolve := func(cmd string) map[string]string {
		leaves := Parse(cmd)
		return InCommandVars(leaves, len(leaves)-1)
	}
	for _, v := range values {
		plain := resolve("WT=" + v + " && git status")

		for _, identical := range []string{"declare", "typeset"} {
			got := resolve(identical + " WT=" + v + " && git status")
			if len(got) == 0 && len(plain) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, plain) {
				t.Errorf("%s WT=%q resolved %v; the plain spelling resolved %v — the two MUST be identical",
					identical, v, got, plain)
			}
		}

		for _, weaker := range []string{
			"local WT=" + v,
			"readonly WT=" + v,
			"nameref WT=" + v,
			"declare -i WT=" + v,
			"declare -l WT=" + v,
			"declare -n WT=" + v,
			"declare -r WT=" + v,
			"declare -a WT=" + v,
			"declare -- WT=" + v,
			"OTHER=/x declare WT=" + v,
		} {
			for name, value := range resolve(weaker + " && git status") {
				plainValue, inPlain := plain[name]
				if !inPlain {
					t.Errorf("%q bound %s=%q, which the plain spelling does not bind at all — that spelling became MORE permissive",
						weaker, name, value)
					continue
				}
				if plainValue != value {
					t.Errorf("%q bound %s=%q where the plain spelling binds %q — a spelling MUST NOT resolve to a DIFFERENT value",
						weaker, name, value, plainValue)
				}
			}
		}
	}
}

// TestInCommandVars_BeforeIsExclusive pins the boundary that keeps a leaf's OWN prefix
// assignments out of its own expansions: the index is the leaf about to be judged, and
// nothing at or after it contributes.
func TestInCommandVars_BeforeIsExclusive(t *testing.T) {
	leaves := Parse(`A=/a && B=/b && git status`)
	if len(leaves) != 3 {
		t.Fatalf("expected 3 leaves, got %d", len(leaves))
	}
	for i, want := range []map[string]string{
		{},
		{"A": "/a"},
		{"A": "/a", "B": "/b"},
	} {
		got := InCommandVars(leaves, i)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("InCommandVars(leaves, %d) = %v, want %v", i, got, want)
		}
	}
	// An out-of-range `before` is clamped rather than panicking: a caller that lost
	// track of the leaf must get the whole environment, never a crash in the hook path.
	if got := InCommandVars(leaves, len(leaves)+5); len(got) != 2 {
		t.Errorf("InCommandVars(leaves, out-of-range) = %v, want both bindings", got)
	}
}

// TestInCommandVars_SubshellScoping is pg2-4ak2k's table: a SUBSHELL scopes its
// assignments (bash forks a child for `( … )`, and a child can never write its parent's
// variables), so a leaf's write must be visible only to leaves whose own subshell scope
// is the SAME as, or an ENCLOSING scope still open at, the consuming leaf's — never to a
// leaf in a scope that has already closed, or a sibling scope, however shallow. The
// closed/sibling rows are the ones a bare nesting-DEPTH counter could not get right (two
// subshells at the same depth can still be different, mutually invisible scopes).
func TestInCommandVars_SubshellScoping(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{
			name: "same subshell: assignment and consumption share one scope",
			cmd:  `(WT=/abs/worktree && git status)`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "enclosing scope: a top-level assignment is visible inside a nested subshell",
			cmd:  `WT=/abs/worktree; (git status)`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			name: "enclosing scope survives two levels of nesting",
			cmd:  `WT=/abs/worktree; ( ( git status ) )`,
			want: map[string]string{"WT": "/abs/worktree"},
		},
		{
			// `( WT=/x ); git status` — bash: $WT is EMPTY here, the subshell already
			// closed. Before this bead this resolved WT anyway (the residual pg2-wq3ki
			// recorded and this bead closes).
			name: "closed subshell: an assignment does not survive its own subshell closing",
			cmd:  `(WT=/abs/worktree); git status`,
			want: nil,
		},
		{
			// Two subshells at the SAME depth are still DIFFERENT scopes — the case a
			// bare depth counter cannot distinguish from the "same subshell" row above.
			name: "sibling subshells never share scope",
			cmd:  `(WT=/abs/worktree); (git status)`,
			want: nil,
		},
		{
			// The critical asymmetry: a closed subshell's write must be COMPLETELY
			// invisible, not merely un-bound — it must not even REVOKE an outer binding
			// of the same name. `WT=/first; (WT=/second); echo "$WT"` prints `/first`.
			name: "a closed subshell's reassignment does not revoke the outer binding it shadowed",
			cmd:  `WT=/first; (WT=/second); git status`,
			want: map[string]string{"WT": "/first"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.cmd)
			got := InCommandVars(leaves, len(leaves)-1)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InCommandVars(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestExpandInCommand covers the ALL-OR-NOTHING contract: anything that does not resolve
// to fully literal text reports ok=false, because a partially expanded path is exactly
// the confident wrong answer the unresolved verdict exists to prevent.
func TestExpandInCommand(t *testing.T) {
	vars := map[string]string{"WT": "/abs/worktree", "EMPTY": "", "REL": "sub"}
	tests := []struct {
		word string
		want string
		ok   bool
	}{
		{word: "/abs/literal", want: "/abs/literal", ok: true},
		{word: "sub/dir", want: "sub/dir", ok: true},
		{word: "$WT", want: "/abs/worktree", ok: true},
		{word: "${WT}", want: "/abs/worktree", ok: true},
		{word: "$WT/nested", want: "/abs/worktree/nested", ok: true},
		{word: "${WT}/nested", want: "/abs/worktree/nested", ok: true},
		{word: "$WT-suffix", want: "/abs/worktree-suffix", ok: true},
		{word: "$REL/x", want: "sub/x", ok: true},
		{word: "$EMPTY", want: "", ok: true},
		{word: "pre/$WT", want: "pre//abs/worktree", ok: true},
		// Unknown names: ABSENT from the environment, never empty.
		{word: "$OTHER", ok: false},
		{word: "$WTX", ok: false},
		{word: "$WT/$OTHER", ok: false},
		// Constructs whose value is not a lookup this seam performed.
		{word: "${WT:-/tmp}", ok: false},
		{word: "${#WT}", ok: false},
		{word: "${WT", ok: false},
		{word: "$1", ok: false},
		{word: "$@", ok: false},
		{word: "$$", ok: false},
		{word: "$(pwd)", ok: false},
		{word: "$", ok: false},
		// Expansions that are not parameter references at all.
		{word: "`pwd`", ok: false},
		{word: "/abs/work*", ok: false},
		{word: "/abs/work?", ok: false},
		{word: "~/repo", ok: false},
		{word: "$WT/*", ok: false},
	}
	for _, tt := range tests {
		got, ok := ExpandInCommand(tt.word, vars)
		if ok != tt.ok {
			t.Errorf("ExpandInCommand(%q) ok = %v, want %v (got %q)", tt.word, ok, tt.ok, got)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("ExpandInCommand(%q) = %q, want %q", tt.word, got, tt.want)
		}
	}
}

// TestExpandInCommand_NoEnvironment pins the pre-pg2-wq3ki behaviour a caller falls back
// to: with no bindings, a literal word passes through and anything needing an expansion
// is refused. Every rule's verdict for the refused shapes is therefore unchanged.
func TestExpandInCommand_NoEnvironment(t *testing.T) {
	for _, word := range []string{"$WT", "${WT}", "$WT/x", "$(pwd)", "`pwd`"} {
		if got, ok := ExpandInCommand(word, nil); ok {
			t.Errorf("ExpandInCommand(%q, nil) = %q, true; want refusal", word, got)
		}
	}
	if got, ok := ExpandInCommand("/abs/literal", nil); !ok || got != "/abs/literal" {
		t.Errorf("ExpandInCommand(literal, nil) = %q, %v; want the word unchanged", got, ok)
	}
}

// TestIsFreshTempDirAssignment pins the narrow DIRECT shape (pg2-d71my): the
// value must be NOTHING BUT a `mktemp -d` / `mktemp --directory` command
// substitution — no literal prefix or suffix, and mktemp WITHOUT a
// directory-creating flag (a FILE, not a directory) does not qualify either.
func TestIsFreshTempDirAssignment(t *testing.T) {
	tests := []struct {
		name string
		ev   EnvAssignment
		want bool
	}{
		{"$(mktemp -d)", EnvAssignment{Name: "T", Value: "$(mktemp -d)", Raw: "T=$(mktemp -d)", Expansion: ExpansionSafeCmd}, true},
		{"backtick mktemp -d", EnvAssignment{Name: "T", Value: "`mktemp -d`", Raw: "T=`mktemp -d`", Expansion: ExpansionSafeCmd}, true},
		{"long flag --directory", EnvAssignment{Name: "T", Value: "$(mktemp --directory)", Raw: "T=$(mktemp --directory)", Expansion: ExpansionSafeCmd}, true},
		{"double-quoted", EnvAssignment{Name: "T", Value: `"$(mktemp -d)"`, Raw: `T="$(mktemp -d)"`, Expansion: ExpansionSafeCmd}, true},
		{"mktemp with no -d creates a FILE", EnvAssignment{Name: "T", Value: "$(mktemp)", Raw: "T=$(mktemp)", Expansion: ExpansionSafeCmd}, false},
		{"a different safe-cmd substitution", EnvAssignment{Name: "T", Value: "$(date +%F)", Raw: "T=$(date +%F)", Expansion: ExpansionSafeCmd}, false},
		{"literal suffix alongside the substitution", EnvAssignment{Name: "T", Value: "$(mktemp -d)/h", Raw: "T=$(mktemp -d)/h", Expansion: ExpansionSafeCmd}, false},
		{"literal prefix alongside the substitution", EnvAssignment{Name: "T", Value: "pre-$(mktemp -d)", Raw: "T=pre-$(mktemp -d)", Expansion: ExpansionSafeCmd}, false},
		{"not SafeCmd at all (var ref)", EnvAssignment{Name: "T", Value: "$OTHER", Raw: "T=$OTHER", Expansion: ExpansionVarRef}, false},
		{"not SafeCmd at all (static)", EnvAssignment{Name: "T", Value: "/tmp/x", Raw: "T=/tmp/x", Expansion: ExpansionNone}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFreshTempDirAssignment(tt.ev); got != tt.want {
				t.Errorf("IsFreshTempDirAssignment(%+v) = %v, want %v", tt.ev, got, tt.want)
			}
		})
	}
}

// TestInCommandTempDirVars_EstablishedBindings covers WHICH leaves establish a
// fresh-temp-dir MARKER for the rest of the expression, and — the load-bearing
// half — that a later reassignment to something that is NOT itself a fresh
// temp dir REVOKES the marker exactly as cmdparse.InCommandVars revokes a
// literal binding.
func TestInCommandTempDirVars_EstablishedBindings(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{
			name: "direct mktemp -d before a command",
			cmd:  `T=$(mktemp -d) && git -C "$T" status`,
			want: map[string]string{"T": ""},
		},
		{
			name: "';' separator",
			cmd:  `T=$(mktemp -d); git status`,
			want: map[string]string{"T": ""},
		},
		{
			name: "export form",
			cmd:  `export T=$(mktemp -d); git status`,
			want: map[string]string{"T": ""},
		},
		{
			name: "backtick form",
			cmd:  "T=`mktemp -d`; git status",
			want: map[string]string{"T": ""},
		},
		{
			name: "an ordinary literal is NOT a temp-dir marker",
			cmd:  `T=/tmp/x; git status`,
			want: nil,
		},
		{
			name: "mktemp WITHOUT -d is NOT a temp-dir marker (a file, not a dir)",
			cmd:  `T=$(mktemp); git status`,
			want: nil,
		},
		{
			name: "a DIFFERENT safe-cmd substitution is NOT a temp-dir marker",
			cmd:  `T=$(date +%F); git status`,
			want: nil,
		},
		{
			name: "revoked by a later literal reassignment",
			cmd:  `T=$(mktemp -d); T=/tmp/other; git status`,
			want: nil,
		},
		{
			name: "revoked by a later non-qualifying substitution",
			cmd:  `T=$(mktemp -d); T=$(date +%F); git status`,
			want: nil,
		},
		{
			name: "two independently-marked variables",
			cmd:  `A=$(mktemp -d); B=$(mktemp -d); git status`,
			want: map[string]string{"A": "", "B": ""},
		},
		{
			name: "a DIFFERENT name is unaffected by an unrelated literal",
			cmd:  `T=$(mktemp -d); OTHER=/tmp/x; git status`,
			want: map[string]string{"T": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.cmd)
			// `before` is the LAST leaf's index: every assignment in the row precedes it.
			got := InCommandTempDirVars(leaves, len(leaves)-1)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InCommandTempDirVars(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestInCommandTempDirVars_BeforeIsExclusive mirrors
// TestInCommandVars_BeforeIsExclusive: a leaf's own assignment must not be
// visible to itself, so `before` at the assigning leaf's OWN index sees
// nothing yet.
func TestInCommandTempDirVars_BeforeIsExclusive(t *testing.T) {
	leaves := Parse(`T=$(mktemp -d); git status`)
	if got := InCommandTempDirVars(leaves, 0); len(got) != 0 {
		t.Errorf("InCommandTempDirVars(leaves, 0) = %v, want empty (leaf 0's own assignment excluded)", got)
	}
	if got := InCommandTempDirVars(leaves, 1); got["T"] != "" {
		t.Errorf(`InCommandTempDirVars(leaves, 1)["T"] = %q, ok=%v; want "", true`, got["T"], got != nil)
	}
}

// TestOverlayVars is the unit-level pin for tc-5h6e's shared merge rule: `local`
// (the nearer scope) shadows `base` (the farther one) name for name, a name only
// `base` defines survives unchanged, and both fast paths (empty local / empty base)
// return the OTHER map's identity rather than allocating — asserted via pointer
// equality, since internal/engine's per-leaf overlay runs on every leaf of every
// evaluated expression and an unnecessary allocation there is a real cost.
func TestOverlayVars(t *testing.T) {
	t.Run("local is empty returns base unchanged (same map, not a copy)", func(t *testing.T) {
		base := map[string]string{"A": "1"}
		got := OverlayVars(base, nil)
		if len(got) != 1 || got["A"] != "1" {
			t.Fatalf("OverlayVars(base, nil) = %v, want %v", got, base)
		}
		// Reflect.DeepEqual isn't a strong enough claim here — the point is identity.
		got["A"] = "mutated"
		if base["A"] != "mutated" {
			t.Error("OverlayVars(base, nil) allocated a copy instead of returning base's own identity")
		}
	})
	t.Run("base is empty returns local unchanged (same map, not a copy)", func(t *testing.T) {
		local := map[string]string{"B": "2"}
		got := OverlayVars(nil, local)
		if len(got) != 1 || got["B"] != "2" {
			t.Fatalf("OverlayVars(nil, local) = %v, want %v", got, local)
		}
		got["B"] = "mutated"
		if local["B"] != "mutated" {
			t.Error("OverlayVars(nil, local) allocated a copy instead of returning local's own identity")
		}
	})
	t.Run("both nil returns nil", func(t *testing.T) {
		if got := OverlayVars(nil, nil); got != nil {
			t.Errorf("OverlayVars(nil, nil) = %v, want nil", got)
		}
	})
	t.Run("local shadows base on a shared name; a base-only name survives", func(t *testing.T) {
		base := map[string]string{"A": "outer", "SHARED": "outer-value"}
		local := map[string]string{"SHARED": "inner-value", "B": "inner"}
		got := OverlayVars(base, local)
		want := map[string]string{"A": "outer", "SHARED": "inner-value", "B": "inner"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("OverlayVars(base, local) = %v, want %v", got, want)
		}
		// NEITHER input is mutated by the merge — a caller holding `base` for a
		// SIBLING leaf still not yet processed must not see this leaf's overlay.
		if base["SHARED"] != "outer-value" || len(base) != 2 {
			t.Errorf("OverlayVars mutated its base argument: %v", base)
		}
		if local["A"] != "" || len(local) != 2 {
			t.Errorf("OverlayVars mutated its local argument: %v", local)
		}
	})
}
