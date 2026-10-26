package cmdparse

import (
	"reflect"
	"testing"
)

// consumerIndex returns the index of the last COMMAND leaf (the consumer in every
// fixture below is the final statement).
func consumerIndex(t *testing.T, leaves []ParsedCommand) int {
	t.Helper()
	for i := len(leaves) - 1; i >= 0; i-- {
		if leaves[i].Executable != "" {
			return i
		}
	}
	t.Fatalf("no command leaf in %+v", leaves)
	return -1
}

// TestStraightLineVars pins bead pg2-niyyq's strict reader: a value is bound only
// when every earlier leaf is an unconditional plain assignment and the consumer sits
// in the main shell's own straight line. Each nil row is a shape in which bash would
// NOT have expanded to the text written down, so binding it would approve a command
// that was never judged.
func TestStraightLineVars(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{"newline separated", "WT=/abs/wt\nbgrun x -- bash -c \"cd $WT\"", map[string]string{"WT": "/abs/wt"}},
		{"semicolon separated", `WT=/abs/wt; bgrun x -- bash -c "cd $WT"`, map[string]string{"WT": "/abs/wt"}},
		{"leftmost operand of an and list", `WT=/abs/wt && bgrun x -- bash -c "cd $WT"`, map[string]string{"WT": "/abs/wt"}},
		{"two assignments", "A=/a\nB=/b\nbgrun x -- bash -c \"$A $B\"", map[string]string{"A": "/a", "B": "/b"}},
		{"later literal wins", "A=/a\nA=/c\nbgrun x -- bash -c \"$A\"", map[string]string{"A": "/c"}},
		{"later non-literal revokes", "A=/a\nA=$(pwd)\nbgrun x -- bash -c \"$A\"", nil},
		{"prefix assignment never binds", `WT=/abs/wt bgrun x -- bash -c "cd $WT"`, nil},
		{"no assignment at all", `bgrun x -- bash -c "cd $WT"`, nil},
		{"conditional or-list", "true || WT=/abs/wt\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"right operand of an and list", "true && WT=/abs/wt\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"compound body", "if true; then WT=/abs/wt; fi\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"subshell", "(WT=/abs/wt)\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"backgrounded", "WT=/abs/wt &\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"pipeline stage", "WT=/abs/wt | cat\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"intervening command", "WT=/abs/wt\nunset WT\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"intervening read", "WT=/abs/wt\nread WT\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"export is not a plain assignment", "export WT=/abs/wt\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"loop variable (data leaf)", "WT=/abs/wt\nfor WT in /etc; do :; done\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"array element write (data leaf)", "WT=/abs/wt\nWT[0]=/etc\nbgrun x -- bash -c \"cd $WT\"", nil},
		{"consumer in a pipeline", "WT=/abs/wt\nbgrun x -- bash -c \"cd $WT\" | cat", nil},
		{"consumer in a loop body", "WT=/abs/wt\nfor d in a; do bgrun x -- bash -c \"cd $WT\"; done", nil},
		{"consumer in a function body", "WT=/abs/wt\nf() { bgrun x -- bash -c \"cd $WT\"; }\nf", nil},
		{"consumer in a subshell", "WT=/abs/wt\n(bgrun x -- bash -c \"cd $WT\")", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := ParseShell(tc.cmd)
			if sp.Unparseable {
				t.Fatalf("unparseable: %s", sp.Reason)
			}
			var got map[string]string
			if tc.name == "consumer in a function body" || tc.name == "consumer in a loop body" || tc.name == "consumer in a subshell" {
				// The consumer is the leaf holding the bgrun payload, wherever it sits.
				for i, l := range sp.Leaves {
					if l.Executable == "bash" {
						got = StraightLineVars(sp.Leaves, i)
					}
				}
			} else {
				got = StraightLineVars(sp.Leaves, consumerIndex(t, sp.Leaves))
				if tc.name == "consumer in a pipeline" {
					for i, l := range sp.Leaves {
						if l.Executable == "bash" {
							got = StraightLineVars(sp.Leaves, i)
						}
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("StraightLineVars(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestStraightLineVarsOutOfRange: a bad index fails closed.
func TestStraightLineVarsOutOfRange(t *testing.T) {
	sp := ParseShell("WT=/abs/wt\nbgrun x -- bash -c 'true'")
	for _, at := range []int{-1, len(sp.Leaves), len(sp.Leaves) + 3} {
		if got := StraightLineVars(sp.Leaves, at); got != nil {
			t.Errorf("at=%d: %v, want nil", at, got)
		}
	}
	if got := StraightLineVars(nil, 0); got != nil {
		t.Errorf("nil leaves: %v, want nil", got)
	}
}

// TestSpineStamps: OnSpine / AlwaysRuns are stamped only on the calls a top-level
// statement's and-or spine is made of, and the leftmost operand alone always runs.
func TestSpineStamps(t *testing.T) {
	type stamp struct{ spine, always bool }
	tests := []struct {
		cmd  string
		want map[string]stamp // by Executable (or "=" for assignment-only)
	}{
		{"a; b", map[string]stamp{"a": {true, true}, "b": {true, true}}},
		{"a && b || c", map[string]stamp{"a": {true, true}, "b": {true, false}, "c": {true, false}}},
		{"a | b", map[string]stamp{"a": {false, false}, "b": {false, false}}},
		{"a &", map[string]stamp{"a": {false, false}}},
		{"(a); b", map[string]stamp{"a": {false, false}, "b": {true, true}}},
		{"if x; then a; fi", map[string]stamp{"x": {false, false}, "a": {false, false}}},
		{"f() { a; }", map[string]stamp{"a": {false, false}}},
		{"X=1", map[string]stamp{"": {true, true}}},
	}
	for _, tc := range tests {
		sp := ParseShell(tc.cmd)
		if sp.Unparseable {
			t.Fatalf("%q unparseable", tc.cmd)
		}
		for _, l := range sp.Leaves {
			w, ok := tc.want[l.Executable]
			if !ok {
				continue
			}
			if l.OnSpine != w.spine || l.AlwaysRuns != w.always {
				t.Errorf("%q leaf %q: OnSpine=%v AlwaysRuns=%v, want %v/%v", tc.cmd, l.Executable, l.OnSpine, l.AlwaysRuns, w.spine, w.always)
			}
		}
	}
}

func TestStraightLineSafeValue(t *testing.T) {
	for v, want := range map[string]bool{
		"/abs/work-tree_1.x": true, "pg2-abc": true, "a:b@c%d+e,f=g": true,
		"": false, "a b": false, "a;b": false, "a$b": false, "a`b": false, "a*b": false,
		"a'b": false, `a"b`: false, "a\nb": false, "a|b": false, "a&b": false, "a(b": false,
		"a>b": false, "~/x": false, "a\\b": false, "a[b": false, "a!b": false, "a{b": false,
	} {
		if got := StraightLineSafeValue(v); got != want {
			t.Errorf("StraightLineSafeValue(%q) = %v, want %v", v, got, want)
		}
	}
}
