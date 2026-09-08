package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestGitWorktreeRemove: the single positional is a PathDelete — the SAME
// access class a plain `rm -rf <worktree-root>` gets — so DeleteAccess's
// worktree-state ladder (internal/effectpolicy) is what judges it, not
// anything in this package. This test only proves the SCHEMA SHAPE: one
// PathDelete effect, `-f`/`--force` inert (repeatable, does not change the
// effect).
func TestGitWorktreeRemove(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	in := GenericInterpreter{}

	got := in.Interpret(leaf(t, "git worktree remove .worktrees/clean"), gitSchema, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: ".worktrees/clean", Access: AccessDelete, Source: "arg 0", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("git worktree remove .worktrees/clean: got %+v, want %+v", got, want)
	}

	force := in.Interpret(leaf(t, "git worktree remove --force .worktrees/dirty"), gitSchema, Context{})
	wantForce := []Effect{
		{Kind: EffectPath, Path: ".worktrees/dirty", Access: AccessDelete, Source: "arg 1", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !force.Sufficient || !reflect.DeepEqual(force.Effects, wantForce) {
		t.Errorf("git worktree remove --force .worktrees/dirty: got %+v, want %+v (force must not change the effect)", force, wantForce)
	}

	noArg := in.Interpret(leaf(t, "git worktree remove"), gitSchema, Context{})
	if noArg.Sufficient {
		t.Errorf("git worktree remove with no operand should be insufficient, got %+v", noArg)
	}
}

// TestGitWorktreePrune: no positional is required; the implicit PathRead of
// ".git/worktrees" fires regardless of -n/-v/--expire, and no delete effect
// is ever emitted (prune's own doc comment on gitWorktreePruneSchema).
func TestGitWorktreePrune(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	in := GenericInterpreter{}
	want := []Effect{
		{Kind: EffectPath, Path: ".git/worktrees", Access: AccessRead, Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}

	for _, cmd := range []string{"git worktree prune", "git worktree prune -n", "git worktree prune -n -v --expire 2.weeks.ago"} {
		got := in.Interpret(leaf(t, cmd), gitSchema, Context{})
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("%q: got %+v, want %+v (no delete effect, ever)", cmd, got, want)
		}
	}
}

// TestGitWorktreeAdd: the leading positional is a PathCreate (mkdirSchema's
// own access class); an optional trailing commit-ish, and -b/-B's branch
// name, are both inert Literal.
func TestGitWorktreeAdd(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	in := GenericInterpreter{}

	got := in.Interpret(leaf(t, "git worktree add .worktrees/new feature"), gitSchema, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: ".worktrees/new", Access: AccessCreate, Source: "arg 0", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("git worktree add .worktrees/new feature: got %+v, want %+v (commit-ish is inert Literal)", got, want)
	}

	branch := in.Interpret(leaf(t, "git worktree add -b feat .worktrees/new"), gitSchema, Context{})
	wantBranch := []Effect{
		{Kind: EffectPath, Path: ".worktrees/new", Access: AccessCreate, Source: "arg 2", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !branch.Sufficient || !reflect.DeepEqual(branch.Effects, wantBranch) {
		t.Errorf("git worktree add -b feat .worktrees/new: got %+v, want %+v (-b's branch name is inert)", branch, wantBranch)
	}

	dyn := in.Interpret(leaf(t, `git worktree add "$D"`), gitSchema, Context{})
	if !dyn.Sufficient || len(dyn.Effects) != 2 || !dyn.Effects[0].Dynamic {
		t.Errorf(`git worktree add "$D": got %+v, want one Dynamic PathCreate effect plus stdout metadata`, dyn)
	}

	noArg := in.Interpret(leaf(t, "git worktree add"), gitSchema, Context{})
	if noArg.Sufficient {
		t.Errorf("git worktree add with no path operand should be insufficient, got %+v", noArg)
	}
}

// TestGitWorktreeLockUnlockRepair: all three are modeled as an ordinary
// PathRead of the worktree operand(s) — reads or metadata-only, per the
// brief; none ever emits a write/delete effect.
func TestGitWorktreeLockUnlockRepair(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	in := GenericInterpreter{}

	lock := in.Interpret(leaf(t, "git worktree lock --reason busy .worktrees/clean"), gitSchema, Context{})
	wantLock := []Effect{{Kind: EffectPath, Path: ".worktrees/clean", Access: AccessRead, Source: "arg 2", FromPositional: true}}
	if !lock.Sufficient || !reflect.DeepEqual(lock.Effects, wantLock) {
		t.Errorf("git worktree lock: got %+v, want %+v", lock, wantLock)
	}

	unlock := in.Interpret(leaf(t, "git worktree unlock .worktrees/clean"), gitSchema, Context{})
	wantUnlock := []Effect{{Kind: EffectPath, Path: ".worktrees/clean", Access: AccessRead, Source: "arg 0", FromPositional: true}}
	if !unlock.Sufficient || !reflect.DeepEqual(unlock.Effects, wantUnlock) {
		t.Errorf("git worktree unlock: got %+v, want %+v", unlock, wantUnlock)
	}

	repairBare := in.Interpret(leaf(t, "git worktree repair"), gitSchema, Context{})
	wantRepairBare := []Effect{{Kind: EffectStdio, Stream: StreamStdout, Metadata: true}}
	if !repairBare.Sufficient || !reflect.DeepEqual(repairBare.Effects, wantRepairBare) {
		t.Errorf("git worktree repair (no paths): got %+v, want %+v", repairBare, wantRepairBare)
	}

	repairPaths := in.Interpret(leaf(t, "git worktree repair .worktrees/clean .worktrees/dirty"), gitSchema, Context{})
	wantRepair := []Effect{
		{Kind: EffectPath, Path: ".worktrees/clean", Access: AccessRead, Source: "arg 0", FromPositional: true},
		{Kind: EffectPath, Path: ".worktrees/dirty", Access: AccessRead, Source: "arg 1", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !repairPaths.Sufficient || !reflect.DeepEqual(repairPaths.Effects, wantRepair) {
		t.Errorf("git worktree repair with paths: got %+v, want %+v", repairPaths, wantRepair)
	}
}

// TestGitWorktreeMove: the old path is PathDelete, the new path is
// PathCreate — move is judged as a delete-plus-create, unlike git mv/rm's
// tc-z806 "recoverable from history" carve-out (a worktree's own git history
// is unaffected by moving where its checkout lives on disk). A third
// positional is Unmodeled (fails closed), not silently swallowed.
func TestGitWorktreeMove(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	in := GenericInterpreter{}

	got := in.Interpret(leaf(t, "git worktree move .worktrees/clean .worktrees/moved"), gitSchema, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: ".worktrees/clean", Access: AccessDelete, Source: "arg 0", FromPositional: true},
		{Kind: EffectPath, Path: ".worktrees/moved", Access: AccessCreate, Source: "arg 1", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("git worktree move: got %+v, want %+v", got, want)
	}

	extra := in.Interpret(leaf(t, "git worktree move a b c"), gitSchema, Context{})
	if extra.Sufficient {
		t.Errorf("git worktree move with a third positional should be insufficient, got %+v", extra)
	}

	tooFew := in.Interpret(leaf(t, "git worktree move a"), gitSchema, Context{})
	if tooFew.Sufficient {
		t.Errorf("git worktree move with only one positional should be insufficient, got %+v", tooFew)
	}
}

// TestGitWorktreeUnknownVerb: an unlisted worktree verb is an unmodeled
// subcommand — no special code, the absent map key is enough (the same
// shape TestSubcommandDispatch's "unknown subcommand" case proves generically).
func TestGitWorktreeUnknownVerb(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "git worktree frobnicate"), gitSchema, Context{})
	if got.Sufficient || !strings.Contains(got.Insufficiency, "unmodeled subcommand frobnicate") {
		t.Errorf("git worktree frobnicate: got %+v", got)
	}
}
