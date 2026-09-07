package cmddesc

import (
	"reflect"
	"testing"
)

// TestApplyTransform: each kind rewrites by effect shape alone, and an
// unknown kind reports false so the interpreter fails closed.
func TestApplyTransform(t *testing.T) {
	posRead := Effect{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true}
	flagRead := Effect{Kind: EffectPath, Path: "s", Access: AccessRead, Source: "arg 0"}
	trunc := Effect{Kind: EffectPath, Path: "d", Access: AccessTruncate, Source: "arg 2", FromPositional: true}
	del := Effect{Kind: EffectPath, Path: "x", Access: AccessDelete, Source: "arg 3", FromPositional: true}
	stdout := Effect{Kind: EffectStdio, Stream: StreamStdout}
	prog := Effect{Kind: EffectProgram, Program: "p", Dialect: "sed"}
	push := Effect{Kind: EffectRemote, Resource: "origin", Operation: "push", Source: "arg 0"}
	all := []Effect{posRead, flagRead, trunc, del, stdout, prog, push}

	with := func(e Effect, a PathAccess) Effect { e.Access = a; return e }
	withOp := func(e Effect, op string) Effect { e.Operation = op; return e }

	cases := []struct {
		name string
		kind TransformKind
		want []Effect
		ok   bool
	}{
		{"none is identity", TransformNone, all, true},
		{"dry-run drops every write class and every remote mutation, keeps reads and non-paths", TransformDryRun, []Effect{posRead, flagRead, stdout, prog}, true},
		{"in-place upgrades positional reads only", TransformInPlace, []Effect{with(posRead, AccessModify), flagRead, trunc, del, stdout, prog, push}, true},
		{"no-clobber turns truncate into create", TransformNoClobber, []Effect{posRead, flagRead, with(trunc, AccessCreate), del, stdout, prog, push}, true},
		{"force rewrites push to force-push", TransformForce, []Effect{posRead, flagRead, trunc, del, stdout, prog, withOp(push, "force-push")}, true},
		{"delete-ref rewrites push to delete-ref", TransformDeleteRef, []Effect{posRead, flagRead, trunc, del, stdout, prog, withOp(push, "delete-ref")}, true},
		{"unknown kind fails closed", TransformKind(99), all, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := applyTransform(EffectTransform{Kind: tc.kind}, all)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestDryRunDropsEveryRemoteMutation: TransformDryRun removes force-push and
// delete-ref too, not just a plain push — the vocabulary check is by
// Operation value, not by whether TransformForce/TransformDeleteRef already
// ran.
func TestDryRunDropsEveryRemoteMutation(t *testing.T) {
	forcePush := Effect{Kind: EffectRemote, Resource: "origin", Operation: "force-push"}
	deleteRef := Effect{Kind: EffectRemote, Resource: "origin", Operation: "delete-ref"}
	read := Effect{Kind: EffectPath, Path: "a", Access: AccessRead}
	got, ok := applyTransform(EffectTransform{Kind: TransformDryRun}, []Effect{forcePush, deleteRef, read})
	if !ok {
		t.Fatal("dry-run reported not ok")
	}
	if !reflect.DeepEqual(got, []Effect{read}) {
		t.Errorf("got %+v, want only the read to survive", got)
	}
}

// TestTransformsApplyInFlagOrder: the interpreter applies transforms in the
// order the flags appeared, so an interacting pair is order-dependent (as
// documented on EffectTransform).
func TestTransformsApplyInFlagOrder(t *testing.T) {
	s := CommandSchema{
		Name: "x",
		Flags: map[string]FlagSpec{
			"-d": {Transform: EffectTransform{Kind: TransformDryRun}},
			"-i": {Transform: EffectTransform{Kind: TransformInPlace}},
		},
		Positionals: PositionalSpec{Rest: PathRead},
	}
	in := GenericInterpreter{}
	access := func(cmd string) []PathAccess {
		var out []PathAccess
		for _, e := range in.Interpret(leaf(t, cmd), s, Context{}).Effects {
			if e.Kind == EffectPath {
				out = append(out, e.Access)
			}
		}
		return out
	}
	if got := access("x -i -d a"); len(got) != 0 {
		t.Errorf("-i then -d: writes should be dropped, got %v", got)
	}
	if got := access("x -d -i a"); !reflect.DeepEqual(got, []PathAccess{AccessModify}) {
		t.Errorf("-d then -i: read should be upgraded after the drop, got %v", got)
	}
}
