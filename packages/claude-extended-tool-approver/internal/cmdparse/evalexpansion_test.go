package cmdparse

import (
	"reflect"
	"testing"
)

// TestEvaluatingExpansions pins which parameter expansions the lowering records on
// ParsedCommand.EvaluatingExpansions (bead pg2-ftneaj): the forms that evaluate their
// operand, and none of the inert ones. It parses with the real ParseShell.
func TestEvaluatingExpansions(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want []string
	}{
		{"prompt transform", `echo "${x@P}"`, []string{"${x@P}"}},
		{"indirection", `echo ${!x}`, []string{"${!x}"}},
		{"variable subscript", `echo "${a[i]}"`, []string{"${a[i]}"}},
		{"variable slice", `echo "${x:n}"`, []string{"${x:n}"}},
		{"two in source order", `echo "${x@P}" "${!y}"`, []string{"${x@P}", "${!y}"}},
		{"in a redirection target", `echo hi > "${x@P}"`, []string{"${x@P}"}},
		{"nested in a default word", `echo "${x:-${y@P}}"`, []string{"${y@P}"}},

		{"plain", `echo "$x" "${x}"`, nil},
		{"default", `echo "${x:-d}" "${x:=d}" "${x:+d}"`, nil},
		{"strip replace length case", `echo "${x#p}" "${x%s}" "${x/a/b}" "${#x}" "${x^^}"`, nil},
		{"literal slice", `echo "${x:1}" "${x:1:2}" "${x: -1}"`, nil},
		{"literal index", `echo "${a[0]}" "${a[@]}" "${a[*]}" "${#a[@]}"`, nil},
		{"key listing", `echo "${!a[@]}"`, nil},
		{"inert transforms", `echo "${x@Q}" "${x@E}" "${x@A}" "${x@a}" "${x@K}" "${x@k}" "${x@L}" "${x@U}" "${x@u}"`, nil},
		{"single quoted is literal", `echo '${x@P}'`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := ParseShell(tt.cmd)
			if sp.Unparseable || len(sp.Leaves) != 1 {
				t.Fatalf("ParseShell(%q): unparseable=%v leaves=%d", tt.cmd, sp.Unparseable, len(sp.Leaves))
			}
			if got := sp.Leaves[0].EvaluatingExpansions; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvaluatingExpansions(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestEvaluatingExpansions_Scoping pins where an evaluating expansion is recorded: a
// substitution body's own leaf owns it (not the outer leaf), an unquoted heredoc body
// owns its own, and a quoted body records none.
func TestEvaluatingExpansions_Scoping(t *testing.T) {
	outer := ParseShell(`echo "$(echo "${x@P}")"`).Leaves[0]
	if len(outer.EvaluatingExpansions) != 0 {
		t.Errorf("outer leaf EvaluatingExpansions = %q, want none (the substitution body's leaf owns it)", outer.EvaluatingExpansions)
	}
	if len(outer.Substitutions) != 1 || len(outer.Substitutions[0].Leaves) != 1 ||
		!reflect.DeepEqual(outer.Substitutions[0].Leaves[0].EvaluatingExpansions, []string{"${x@P}"}) {
		t.Errorf("substitution body leaf did not record ${x@P}: %+v", outer.Substitutions)
	}

	unq := ParseShell("cat <<EOF\n${x@P}\nEOF").Leaves[0]
	if got := unq.UnquotedHeredocEvaluatingExpansions(); !reflect.DeepEqual(got, []string{"${x@P}"}) {
		t.Errorf("unquoted heredoc EvaluatingExpansions = %q, want [${x@P}]", got)
	}
	if len(unq.EvaluatingExpansions) != 0 {
		t.Errorf("heredoc body expansion leaked onto the leaf: %q", unq.EvaluatingExpansions)
	}

	quo := ParseShell("cat <<'EOF'\n${x@P}\nEOF").Leaves[0]
	if got := quo.UnquotedHeredocEvaluatingExpansions(); len(got) != 0 {
		t.Errorf("quoted heredoc EvaluatingExpansions = %q, want none", got)
	}
}
