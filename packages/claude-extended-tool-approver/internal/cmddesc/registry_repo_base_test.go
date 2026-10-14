package cmddesc

import "testing"

// TestRepoBasePnPnwfInterpretation pins the pg2-cjfpy.3 schemas: every
// pn/pnwf form the phillipg-nix-repo-base plugins instruct interprets as
// sufficient, that exactly the verbs which run workspace code (pg2-qy5u8)
// declare one Exec fact and every other verb has NO effect beyond stdout
// (see registry_repo_base.go's "Effect model"), and the deliberately-unmodeled
// forms stay insufficient so they abstain.
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
	// The verbs that run the workspace's own code: each declares exactly one
	// EffectExec (judged by effectpolicy.TrustedCheckoutExec). Every other
	// sufficient form above is read-only introspection or a worktree/lifecycle
	// helper with no effect beyond stdout.
	execForms := map[string]string{
		"pn workspace build":                   "pn workspace build",
		"pn workspace flake-check":             "pn workspace flake-check",
		"pn workspace format":                  "pn workspace format",
		"pn workspace pre-commit-check":        "pn workspace pre-commit-check",
		"pn workspace doctor":                  "pn workspace doctor",
		"pn workspace doctor --json":           "pn workspace doctor",
		"pn workspace update":                  "pn workspace update",
		"pn workspace update --in-place":       "pn workspace update",
		"pn workspace update --siblings-only":  "pn workspace update",
		"pn workspace rebase":                  "pn workspace rebase",
		"pn workspace rebase main":             "pn workspace rebase",
		"pn workspace push":                    "pn workspace push",
		"pn workspace push --no-siblings":      "pn workspace push",
		"pn workspace push -u --remote origin": "pn workspace push",
		"pn workspace apply":                   "pn workspace apply",
		"pn workspace lock":                    "pn workspace lock",
		"pn workspace clone":                   "pn workspace clone",
		"pn workspace build --terminal repo-a": "pn workspace build",
		"pnwf sync-fetch --set":                "pnwf sync-fetch",
		"pnwf update-relock --set":             "pnwf update-relock",
		"pnwf cleanup my-branch":               "pnwf cleanup",
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
		want, isExec := execForms[cmd]
		execs := 0
		for _, e := range in.Effects {
			switch {
			case e.Kind == EffectStdio:
			case e.Kind == EffectExec && isExec && e.Source == want && !e.Dynamic && e.Family == "":
				execs++
			default:
				t.Errorf("%q: want no effect beyond stdio (and, for a workspace-code verb, one %q exec), got %+v", cmd, want, e)
			}
		}
		if isExec && execs != 1 {
			t.Errorf("%q: want exactly one exec fact %q, got %d", cmd, want, execs)
		}
	}
	for _, cmd := range insufficient {
		if in := run(cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
}
