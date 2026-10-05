package cmddesc

import "testing"

// TestPbInterpretation pins the pg2-cjfpy.1 schema: every pb form the
// claude-marketplace/pb plugin instructs interprets as sufficient with NO
// effect beyond stdout (pb is modeled as an inert-literal command; see
// registry_pb.go), and the deliberately-unmodeled forms stay insufficient so
// they abstain.
func TestPbInterpretation(t *testing.T) {
	reg := DefaultRegistry()
	sufficient := []string{
		"pb drain isolate --bead pg2-abc12 --repo /ws/repo",
		"pb drain isolate --bead pg2-abc12 --repo /ws/repo --json",
		"pb drain isolate --help",
		"pb gate create --blocks pg2-abc12 --repo repo-a",
		"pb gate create --blocks pg2-abc12 --repo repo-a --commit HEAD",
		"pb gate create --blocks pg2-abc12 --repo repo-a --commits main..HEAD --reason why --json",
		"pb gate check",
		"pb gate check --dry-run",
		"pb gate check --dry-run --json",
		"pb gate attach-verified-child --impl pg2-abc12 --title t --gate repo-a=0123abc --actor a-drain",
		"pb gate attach-verified-child --impl pg2-abc12 --title t --gate repo-a=0123abc --gate repo-b=4567def --actor a-drain --json",
		"pb gate attach-verified-child --impl pg2-abc12 --title t --gate repo-a=0123abc --actor a-drain --reason why",
	}
	insufficient := []string{
		// No subcommand at all (a bare group, or only a help flag): the
		// generic subcommand dispatcher cannot name a schema to apply.
		"pb",
		"pb gate",
		"pb gate --help",
		"pb drain",
		// Flags no skill or command instructs stay unmodeled.
		"pb gate check --stale-handler close",
		"pb gate check --stale-after 1d",
		"pb gate check --last-n 5",
		"pb gate check --strict",
		"pb drain isolate --bead pg2-abc12 --repo /ws/repo --force",
		// A stray operand is Unmodeled.
		"pb gate check extra",
		"pb drain isolate --bead pg2-abc12 --repo /ws/repo extra",
		// Unmodeled verbs.
		"pb completion zsh",
		"pb help",
		"pb frobnicate",
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
