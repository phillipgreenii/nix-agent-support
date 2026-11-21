package cmddesc

import "testing"

// TestPgGitLanderInterpretation pins the pg-git-lander schema: which forms are
// sufficient, which effects each declares, and that --no-verify (like git's)
// and every unlisted flag leave the call insufficient.
func TestPgGitLanderInterpretation(t *testing.T) {
	reg := DefaultRegistry()
	run := func(cmd string) Interpretation {
		l := leaf(t, cmd)
		schema, ok := reg.Lookup(l.Executable)
		if !ok {
			t.Fatalf("%q: executable %q not registered", cmd, l.Executable)
		}
		in, ok := LookupInterpreter(schema.Interpreter)
		if !ok {
			t.Fatalf("%q: no interpreter", cmd)
		}
		return in.Interpret(l, schema, Context{})
	}
	sufficient := []string{
		"pg-git-lander status",
		"pg-git-lander status drain/x --json",
		"pg-git-lander log drain/x --tail 20 --repo /repo",
		"pg-git-lander config check --file /repo/x.toml --json",
		"pg-git-lander land drain/x --repo /repo --source-worktree /repo/.worktrees/x --wait 20m --json",
		"pg-git-lander land drain/x --keep-source-worktree --keep-source-branch",
		"pg-git-lander land --continue --json",
		"pg-git-lander land drain/x --abort",
		"pg-git-lander land drain/x --abort --repo /repo",
		"pg-git-lander land drain/x --quit --repo /repo",
		"pg-git-lander land drain/x --continue --repo /repo",
		"pg-git-lander rebase drain/x --abort --repo /repo",
		"pg-git-lander rebase drain/x --onto main --context k=v",
	}
	for _, cmd := range sufficient {
		if in := run(cmd); !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
		}
	}
	insufficient := []string{
		"pg-git-lander",
		"pg-git-lander frobnicate",
		"pg-git-lander land drain/x --no-verify",
		"pg-git-lander land --continue --no-verify",
		"pg-git-lander land --skip --no-verify",
		"pg-git-lander rebase drain/x --onto main --no-verify",
		"pg-git-lander land drain/x --frobnicate",
		"pg-git-lander status --no-verify",
		"pg-git-lander land drain/x --abort --no-verify",
		"pg-git-lander land drain/x --abort --repo",
		"pg-git-lander land drain/x --quit --frobnicate",
		"pg-git-lander status --abort --frobnicate",
	}
	for _, cmd := range insufficient {
		if in := run(cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
	// The read-only verbs declare no Exec fact; rebase and land declare exactly one.
	hasExec := func(cmd string) int {
		n := 0
		for _, e := range run(cmd).Effects {
			if e.Kind == EffectExec {
				n++
			}
		}
		return n
	}
	for _, cmd := range []string{"pg-git-lander status", "pg-git-lander log drain/x", "pg-git-lander config check"} {
		if n := hasExec(cmd); n != 0 {
			t.Errorf("%q: read-only verb declares %d exec facts, want 0", cmd, n)
		}
	}
	for _, cmd := range []string{"pg-git-lander land drain/x", "pg-git-lander rebase drain/x --onto main", "pg-git-lander land --continue"} {
		if n := hasExec(cmd); n != 1 {
			t.Errorf("%q: declares %d exec facts, want exactly 1", cmd, n)
		}
	}
	// land states the branch deletion and the source-worktree deletion.
	in := run("pg-git-lander land drain/x --source-worktree /repo/.worktrees/x")
	var delWT, modGit bool
	for _, e := range in.Effects {
		if e.Kind == EffectPath && e.Path == "/repo/.worktrees/x" && e.Access == AccessDelete {
			delWT = true
		}
	}
	for _, e := range in.Effects {
		if e.Kind == EffectPath && e.Path == ".git" && e.Access == AccessModify {
			modGit = true
		}
	}
	if !delWT || !modGit {
		t.Errorf("land: want source-worktree delete (%v) and .git modify (%v) effects, got %+v", delWT, modGit, in.Effects)
	}
}
