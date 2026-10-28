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

		// arithmetic evaluation of a variable (bead pg2-giluda)
		{"arith variable", `echo "$((x))"`, []string{"$((x))"}},
		{"arith expanded variable", `echo "$(($x + 1))"`, []string{"$(($x + 1))"}},
		{"arith mixed", `echo $((1 + y))`, []string{"$((1 + y))"}},
		{"arith assignment", `echo $((x=1))`, []string{"$((x=1))"}},
		{"arith legacy bracket form", `echo "$[x]"`, []string{"$[x]"}},
		{"arith subscript operand", `echo "$((a[i]))"`, []string{"$((a[i]))", "a[i]"}},
		{"arith in a redirection target", `echo hi > "f$((x))"`, []string{"$((x))"}},
		{"arith nested in a default word", `echo "${x:-$((y))}"`, []string{"$((y))"}},

		{"arith literal", `echo "$((1+2))" $((3 * (4 - 1))) $((0x10)) $((1 ? 2 : 3))`, nil},
		{"arith legacy literal", `echo "$[1+2]"`, nil},
		{"arith with command substitution operands", `echo "$(( $(date +%s) - $(cat f) ))" "$(( 1 + $(( $(cat g) )) ))"`, nil},
		{"arith command substitution plus variable", `echo "$(( $(date +%s) - n ))"`, []string{"$(( $(date +%s) - n ))"}},
		{"arith single quoted is literal", `echo '$((x))'`, nil},

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

// TestEvaluatingExpansions_ArithmeticStatements pins the arithmetic forms that are
// statements or `[[ ]]` clauses rather than expansions in an argument: they reach a data
// leaf, which records the same EvaluatingExpansions (bead pg2-giluda).
func TestEvaluatingExpansions_ArithmeticStatements(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want []string
	}{
		{"arithmetic command variable", `(( x ))`, []string{"(( x ))"}},
		{"arithmetic command increment", `(( i++ ))`, []string{"(( i++ ))"}},
		{"let variable", `let x`, []string{"let x"}},
		{"test -eq variable", `[[ $x -eq 1 ]]`, []string{"$x -eq 1"}},
		{"test -lt bare name", `[[ n -lt 3 ]]`, []string{"n -lt 3"}},
		{"test -v subscript", `[[ -v a[i] ]]`, []string{"-v a[i]"}},
		{"test -v indirect", `[[ -v $x ]]`, []string{"-v $x"}},

		{"assignment-only arithmetic", `y=$((x))`, []string{"$((x))"}},
		{"assignment-only prompt transform", `y="${x@P}"`, []string{"${x@P}"}},
		{"leading assignment arithmetic", `FOO=$((x)) echo hi`, []string{"$((x))"}},
		{"leading assignment prompt transform", `FOO="${x@P}" echo hi`, []string{"${x@P}"}},
		{"subscript assignment", `a[i]=1`, []string{"a[i]=1", "a[i]=1"}},

		{"assignment literal arithmetic", `y=$((1+2))`, nil},
		{"assignment plain value", `y=$x z=${w:-d} echo hi`, nil},
		{"literal subscript assignment", `a[1]=2`, nil},
		{"arithmetic command literal", `(( 1 + 2 ))`, nil},
		{"let literal", `let 1+2`, nil},
		{"test -eq literal", `[[ 1 -eq 1 ]]`, nil},
		{"test -v plain name", `[[ -v x ]]`, nil},
		{"test string compare", `[[ $x == y ]]`, nil},
		{"test file check", `[[ -f $x ]]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := ParseShell(tt.cmd)
			if sp.Unparseable {
				t.Fatalf("ParseShell(%q) unparseable", tt.cmd)
			}
			var got []string
			for _, l := range sp.Leaves {
				got = append(got, l.EvaluatingExpansions...)
			}
			if !reflect.DeepEqual(got, tt.want) {
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
