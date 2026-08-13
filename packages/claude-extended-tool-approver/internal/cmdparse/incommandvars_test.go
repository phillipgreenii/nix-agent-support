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
			name: "declare/local keep their assignment in Args, not EnvVars",
			cmd:  `declare WT=/abs/worktree && git status`,
			want: nil,
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
