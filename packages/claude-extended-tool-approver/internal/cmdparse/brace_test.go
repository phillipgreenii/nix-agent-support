package cmdparse

import (
	"reflect"
	"testing"
)

// TestBraceExpansion pins item g's brace-expansion acceptance bar (docket
// tc-o14i5.3, Phase 2): a brace expression already expanded by an outer
// shell (the shape Claude Code's own tool_input delivery may already
// provide) and the same expression arriving LITERAL and unexpanded must both
// lower to the correct, equivalent set of argv entries.
func TestBraceExpansion(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantExec string
		wantArgs []string
	}{
		// --- already-expanded form: nothing for this lowering to do, the
		// argv positions are already separate tokens. ---
		{"already expanded", "echo a.txt b.txt", "echo", []string{"a.txt", "b.txt"}},

		// --- literal, unexpanded form: this lowering must expand it itself,
		// to the SAME argv set as the already-expanded case above. ---
		{"literal list form expands to equivalent argv", "echo {a,b}.txt", "echo", []string{"a.txt", "b.txt"}},
		{"literal form with a leading prefix too", "cp file{a,b}.txt dest/", "cp", []string{"filea.txt", "fileb.txt", "dest/"}},
		{"three-way list", "rm {a,b,c}.txt", "rm", []string{"a.txt", "b.txt", "c.txt"}},

		// --- sequence form: {x..y} and {x..y..incr} ---
		{"numeric sequence", "echo file{1..3}.txt", "echo", []string{"file1.txt", "file2.txt", "file3.txt"}},
		{"numeric sequence with increment", "echo n{0..6..2}", "echo", []string{"n0", "n2", "n4", "n6"}},
		{"letter sequence", "echo {a..c}.txt", "echo", []string{"a.txt", "b.txt", "c.txt"}},

		// --- brace expansion applies to the EXECUTABLE position too: bash
		// expands every word on the line before deciding roles, so the first
		// resulting word becomes the executable and any further ones shift
		// into args, exactly like any other multi-word expansion would. ---
		{"executable position expands too", "{echo,cat} foo", "echo", []string{"cat", "foo"}},

		// --- quoting fences brace expansion: bash performs NO expansion
		// inside single or double quotes, so a quoted brace must survive as
		// ONE literal argument, not be expanded. ---
		{"double-quoted braces are literal", `echo "{a,b}"`, "echo", []string{"{a,b}"}},
		{"single-quoted braces are literal", `echo '{a,b}'`, "echo", []string{"{a,b}"}},

		// --- malformed / non-brace shapes bash itself does not expand ---
		{"single element is not a brace", "echo {a}", "echo", []string{"{a}"}},
		{"unterminated brace stays literal", "echo {a", "echo", []string{"{a"}},
		{"empty braces stay literal", "echo {}.txt", "echo", []string{"{}.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.in)
			if len(leaves) != 1 {
				t.Fatalf("Parse(%q) = %d leaves, want 1: %+v", tt.in, len(leaves), leaves)
			}
			got := leaves[0]
			if got.Executable != tt.wantExec {
				t.Errorf("Parse(%q).Executable = %q, want %q", tt.in, got.Executable, tt.wantExec)
			}
			if !reflect.DeepEqual(got.Args, tt.wantArgs) {
				t.Errorf("Parse(%q).Args = %v, want %v", tt.in, got.Args, tt.wantArgs)
			}
			if len(got.ArgLiveExpansion) != len(got.Args) {
				t.Errorf("Parse(%q): ArgLiveExpansion has %d entries, want %d (one per Args entry)",
					tt.in, len(got.ArgLiveExpansion), len(got.Args))
			}
		})
	}
}

// TestBraceExpansion_LiveAndLiteralFormsAgree is the equivalence assertion
// item g's own wording states directly: the live-expanded form and the
// literal-unexpanded form of the SAME command must lower to the identical
// Executable/Args.
func TestBraceExpansion_LiveAndLiteralFormsAgree(t *testing.T) {
	tests := []struct {
		name     string
		expanded string
		literal  string
	}{
		{"simple list", "cp file.txt file.bak", "cp file.{txt,bak}"},
		{"three files", "rm a.log b.log c.log", "rm {a,b,c}.log"},
		{"numeric sequence", "echo 1 2 3", "echo {1..3}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expandedLeaves := Parse(tt.expanded)
			literalLeaves := Parse(tt.literal)
			if len(expandedLeaves) != 1 || len(literalLeaves) != 1 {
				t.Fatalf("expected exactly one leaf each: expanded=%d literal=%d", len(expandedLeaves), len(literalLeaves))
			}
			if expandedLeaves[0].Executable != literalLeaves[0].Executable {
				t.Errorf("Executable differs: expanded form %q (%q) = %q, literal form %q (%q) = %q",
					tt.expanded, tt.expanded, expandedLeaves[0].Executable, tt.literal, tt.literal, literalLeaves[0].Executable)
			}
			if !reflect.DeepEqual(expandedLeaves[0].Args, literalLeaves[0].Args) {
				t.Errorf("Args differ: expanded form %q = %v, literal form %q = %v",
					tt.expanded, expandedLeaves[0].Args, tt.literal, literalLeaves[0].Args)
			}
		})
	}
}

// TestBraceExpansion_ProcessSubstitutionInsideBraceElement pins that a
// process substitution embedded inside one brace alternative is still
// lifted into ProcessSubstitutions for THAT alternative's own argv position,
// exactly as an ordinary (non-brace) word carrying one already is — brace
// support must not create a new blind spot for I12/I13.
func TestBraceExpansion_ProcessSubstitutionInsideBraceElement(t *testing.T) {
	leaves := Parse("diff <(sort a) {b,<(sort c)}")
	if len(leaves) != 1 {
		t.Fatalf("Parse = %d leaves, want 1: %+v", len(leaves), leaves)
	}
	got := leaves[0]
	wantArgs := []string{procSubFabricatedOperand, "b", procSubFabricatedOperand}
	if !reflect.DeepEqual(got.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", got.Args, wantArgs)
	}
	wantSubs := []string{"sort a", "sort c"}
	if !reflect.DeepEqual(got.ProcessSubstitutions, wantSubs) {
		t.Errorf("ProcessSubstitutions = %v, want %v", got.ProcessSubstitutions, wantSubs)
	}
}
