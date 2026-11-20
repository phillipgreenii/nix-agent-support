package cmddesc

import "testing"

// TestPgGwmInterpretation pins the pg-gwm schema against the real registry:
// every verb's class (read-only, lease-class, operator-class), the documented
// command-substitution and --format=json forms, and that --repo is path-checked
// like git -C (a CommandDir effect).
func TestPgGwmInterpretation(t *testing.T) {
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
		// read-only
		"pg-gwm list",
		"pg-gwm list --include-unmanaged --format=json",
		"pg-gwm list --json --repo /repo",
		"pg-gwm info --name x",
		"pg-gwm info --path /repo/.worktrees/x --print=path",
		"pg-gwm info some-key --format json",
		"pg-gwm preserved list",
		"pg-gwm preserved list --json",
		"pg-gwm config check",
		"pg-gwm config check --config /repo/pg-gwm.toml",
		"pg-gwm reap --dry-run",
		"pg-gwm reap --dry-run --all-repos --deadline 10m --format=json",
		"pg-gwm completion bash",
		"pg-gwm version",
		// lease-class
		"pg-gwm lease --name x -b feature/x --print=path",
		"pg-gwm lease --detach origin/main --wait 2m",
		"pg-gwm lease --name x --or-renew --ttl 30m --owner me --metadata k=v",
		"pg-gwm lease -B feature/x --repo /repo",
		"pg-gwm renew --name x --ttl 30m --print=path",
		"pg-gwm renew lease-id-1",
		"pg-gwm release --name x",
		"pg-gwm release lease-id-1 --json",
		"pg-gwm expire --name x",
	}
	for _, cmd := range sufficient {
		if in := run(cmd); !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
		}
	}
	insufficient := []string{
		"pg-gwm",
		"pg-gwm frobnicate",
		// operator-class
		"pg-gwm reap",
		"pg-gwm reap --all-repos",
		"pg-gwm reap --all-repos --deadline 10m",
		"pg-gwm reap --dry-run=false",
		"pg-gwm reap -dry-run",
		"pg-gwm preserved prune",
		"pg-gwm preserved prune --older-than 30d --dry-run",
		"pg-gwm serve",
		"pg-gwm serve --addr 127.0.0.1:9148",
		// unknown flags and shapes
		"pg-gwm list --frobnicate",
		"pg-gwm lease --frobnicate",
		"pg-gwm lease -name x",
		"pg-gwm preserved",
		"pg-gwm config",
	}
	for _, cmd := range insufficient {
		if in := run(cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
	// --repo is the path-checked working directory, like git -C.
	for _, cmd := range []string{"pg-gwm list --repo /elsewhere", "pg-gwm lease --name x --repo /elsewhere"} {
		var found bool
		for _, e := range run(cmd).Effects {
			if e.Kind == EffectPath && e.Path == "/elsewhere" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want a path effect on --repo /elsewhere", cmd)
		}
	}
	// Verbs state no Exec fact: they approve in any session cwd.
	for _, cmd := range []string{"pg-gwm lease --name x", "pg-gwm list", "pg-gwm reap --dry-run"} {
		for _, e := range run(cmd).Effects {
			if e.Kind == EffectExec {
				t.Errorf("%q: declares an exec fact %+v", cmd, e)
			}
		}
	}
}
