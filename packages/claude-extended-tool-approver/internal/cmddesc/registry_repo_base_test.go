package cmddesc

import "testing"

// TestRepoBasePnPnwfInterpretation pins the pg2-cjfpy.3 schemas: every
// pn/pnwf form the phillipg-nix-repo-base plugins instruct interprets as
// sufficient with NO effect beyond stdout (these tools are modeled as inert-literal
// commands; see registry_repo_base.go), and the deliberately-unmodeled forms
// stay insufficient so they abstain.
func TestRepoBasePnPnwfInterpretation(t *testing.T) {
	reg := DefaultRegistry()
	sufficient := []string{
		"pn workspace build",
		"pn workspace flake-check",
		"pn workspace format",
		"pn workspace pre-commit-check",
		"pn workspace doctor",
		"pn workspace doctor --json",
		"pn workspace status",
		"pn workspace tree",
		"pn workspace discover",
		"pn workspace info --json",
		"pn workspace update",
		"pn workspace update --in-place",
		"pn workspace update --siblings-only",
		"pn workspace rebase",
		"pn workspace rebase main",
		"pn workspace push",
		"pn workspace push --no-siblings",
		"pn workspace push -u --remote origin",
		"pn workspace apply",
		"pn workspace lock",
		"pn workspace clone",
		"pn workspace build --terminal repo-a",
		"pn workspace workforest add my-feature",
		"pn workspace workforest add my-feature --repos a,b",
		"pn workspace workforest add-repo my-feature repo-a",
		"pn workspace workforest list",
		"pn workspace workforest prune",
		"pnwf resolve",
		"pnwf resolve --set",
		"pnwf repos --set",
		"pnwf stage --set",
		"pnwf fork-preflight my-branch --repos a,b",
		"pnwf land-plan my-branch",
		"pnwf status my-branch",
		"pnwf residue --set",
		"pnwf sync-fetch --set",
		"pnwf update-relock --set",
		"pnwf cleanup my-branch",
	}
	insufficient := []string{
		"pn workspace init",
		"pn workspace allow",
		"pn workspace deny",
		"pn workspace upgrade",
		"pn workspace nix -- build .#x",
		"pn workspace workforest remove my-feature",
		"pn workspace workforest remove-repo my-feature repo-a",
		"pn workspace push --no-verify",
		"pn workspace doctor --fix",
		"pn workspace build --otlp-endpoint http://collector",
		"pn workspace",
		"pnwf cleanup my-branch --force-dirty-worktree-removal",
		"pnwf cleanup my-branch --force-unlanded-branch-removal",
		"pnwf frobnicate",
	}
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
	for _, cmd := range sufficient {
		in := run(cmd)
		if !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
		}
		for _, e := range in.Effects {
			if e.Kind != EffectStdio {
				t.Errorf("%q: want no effect beyond stdio (inert model), got %+v", cmd, e)
			}
		}
	}
	for _, cmd := range insufficient {
		if in := run(cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
}
