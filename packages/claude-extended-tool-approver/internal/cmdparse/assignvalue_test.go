package cmdparse

import (
	"reflect"
	"strings"
	"testing"
)

// TestAssignmentValueSubstitutions (pg2-dbrsg): the seam that hands an
// env-assignment value's command substitutions, pre-lowered, to a consumer
// that must grade them like any other command. Every row states the exact
// bodies it must surface and, for a value it does NOT model, a reason
// fragment — an unmodeled value MUST NOT be reported as understood.
func TestAssignmentValueSubstitutions(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantBodies []string
		wantReason string // non-empty: the value is unmodeled, reason must contain this
	}{
		{name: "static", value: "/abs/path"},
		{name: "empty", value: ""},
		{name: "single-quoted dollar paren is literal", value: `'$(rm -rf x)'`},
		{name: "plain param ref", value: `"$WT/sub"`},
		{name: "braced param ref", value: `${WT}`},
		{name: "default word param", value: `${X:-fallback}`},
		{name: "trim param", value: `${X#prefix}`},
		{name: "length param", value: `${#X}`},
		{name: "positional params", value: `"$1"`},
		{name: "command substitution", value: `$(git rev-parse HEAD)`, wantBodies: []string{"git rev-parse HEAD"}},
		{name: "quoted command substitution", value: `"$(git -C /r rev-parse HEAD)"`, wantBodies: []string{"git -C /r rev-parse HEAD"}},
		{name: "backtick substitution", value: "`pwd`", wantBodies: []string{"pwd"}},
		{name: "two substitutions in one value", value: `"$(a)-$(b)"`, wantBodies: []string{"a", "b"}},
		{name: "substitution beside a param", value: `"$ROOT/$(basename x)"`, wantBodies: []string{"basename x"}},
		{name: "substitution in a default word", value: `${X:-$(date +%F)}`, wantBodies: []string{"date +%F"}},
		{name: "nested substitution stays inside the outer body", value: `$(echo $(pwd))`, wantBodies: []string{"echo $(pwd)"}},
		{name: "array value with substitution", value: `(a $(b))`, wantBodies: []string{"b"}},

		{name: "arithmetic", value: `$((1+2))`, wantReason: "arithmetic"},
		{name: "arithmetic hiding a substitution", value: `$(( $(rm x) ))`, wantReason: "arithmetic"},
		{name: "arithmetic beside a substitution", value: `$(a)$((1))`, wantReason: "arithmetic"},
		{name: "process substitution in", value: `<(cat f)`, wantReason: "process substitution"},
		{name: "process substitution out", value: `>(tee f)`, wantReason: "process substitution"},
		{name: "indirect expansion", value: `${!X}`, wantReason: "indirect"},
		{name: "subscripted expansion", value: `${a[$i]}`, wantReason: "subscripted"},
		{name: "sliced expansion", value: `${X:1:2}`, wantReason: "subscripted or sliced"},
		{name: "prompt transform", value: `${X@P}`, wantReason: "transforming"},
		{name: "unterminated substitution", value: `$(oops`, wantReason: "not parseable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			subs, reason := AssignmentValueSubstitutions(tc.value)
			if tc.wantReason != "" {
				if reason == "" || !strings.Contains(reason, tc.wantReason) {
					t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantReason)
				}
				if len(subs) != 0 {
					t.Errorf("an unmodeled value must surface no substitutions, got %+v", subs)
				}
				return
			}
			if reason != "" {
				t.Fatalf("unexpected unmodeled reason %q", reason)
			}
			var bodies []string
			for _, s := range subs {
				bodies = append(bodies, s.Body)
				if len(s.Leaves) == 0 {
					t.Errorf("substitution %q has no pre-lowered leaves", s.Body)
				}
			}
			if !reflect.DeepEqual(bodies, tc.wantBodies) {
				t.Errorf("bodies = %q, want %q", bodies, tc.wantBodies)
			}
		})
	}
}

// TestAssignmentValueSubstitutions_LeavesAreTheBodyLeaves: the pre-lowered
// leaves are the body's own, so a consumer grading them judges the same
// command it would have judged at the top level.
func TestAssignmentValueSubstitutions_LeavesAreTheBodyLeaves(t *testing.T) {
	subs, reason := AssignmentValueSubstitutions(`"$(git -C /r status --porcelain | wc -l)"`)
	if reason != "" || len(subs) != 1 {
		t.Fatalf("subs=%+v reason=%q", subs, reason)
	}
	var exes []string
	for _, l := range subs[0].Leaves {
		exes = append(exes, l.Executable)
	}
	if !reflect.DeepEqual(exes, []string{"git", "wc"}) {
		t.Errorf("leaf executables = %q, want [git wc]", exes)
	}
}

// TestDataKind (pg2-dbrsg): each command-less DATA leaf records which
// construct produced it, because a consumer may treat only some kinds as
// inert. A command leaf, an assignment-only leaf and a redirection-only leaf
// are NOT data leaves.
func TestDataKind(t *testing.T) {
	tests := []struct {
		command string
		raw     string
		want    DataKind
	}{
		{`for f in a b; do echo $f; done`, "a b", DataWordList},
		{`case "$X" in a) echo hi;; esac`, `"$X"`, DataCaseWord},
		{`case "$X" in a) echo hi;; esac`, "a", DataCasePattern},
		{`[[ -z "$X" ]]`, `[[ -z "$X" ]]`, DataTest},
		{`(( i++ ))`, "(( i++ ))", DataArithmetic},
		{`let a=b`, "let a=b", DataArithmetic},
		{`a[$i]=v`, "a[$i]=v", DataOther},
		{`""`, `""`, DataOther},
	}
	for _, tc := range tests {
		t.Run(tc.command+"/"+tc.raw, func(t *testing.T) {
			var got *ParsedCommand
			sp := ParseShell(tc.command)
			for i := range sp.Leaves {
				if sp.Leaves[i].Raw == tc.raw {
					got = &sp.Leaves[i]
				}
			}
			if got == nil {
				t.Fatalf("no leaf with Raw %q in %+v", tc.raw, sp.Leaves)
			}
			if got.Data != tc.want {
				t.Errorf("Data = %d, want %d", got.Data, tc.want)
			}
		})
	}
	for _, cmd := range []string{`ls`, `X=1`, `> f`, `X=$(a)`, `export X=1`} {
		for _, l := range ParseShell(cmd).Leaves {
			if l.Data != DataNone {
				t.Errorf("%q: leaf %q is not a data leaf but has Data=%d", cmd, l.Raw, l.Data)
			}
		}
	}
}
