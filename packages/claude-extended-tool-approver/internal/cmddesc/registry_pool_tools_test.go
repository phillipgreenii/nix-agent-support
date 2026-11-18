package cmddesc

import "testing"

func interpretForPoolTools(t *testing.T, cmd string) Interpretation {
	t.Helper()
	l := leaf(t, cmd)
	schema, ok := DefaultRegistry().Lookup(l.Executable)
	if !ok {
		t.Fatalf("%q: executable %q not registered", cmd, l.Executable)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("%q: no interpreter", cmd)
	}
	return in.Interpret(l, schema, Context{})
}

// TestCcpoolInterpretation pins the ccpool schema: the read-only listing and
// status verbs are sufficient with no effect beyond the --pool read and stdio,
// trust states its ~/.claude.json write, and the verbs that drive another agent
// session or change pool state are absent (insufficient).
func TestCcpoolInterpretation(t *testing.T) {
	sufficient := []string{
		"ccpool list",
		"ccpool list --json",
		"ccpool list --all --json --state needs_input",
		"ccpool list --paths --filter k=v",
		"ccpool list --help",
		"ccpool --help",
		"ccpool --pool /Users/u/.local/state/pool list --json",
		"ccpool state abc --json",
		"ccpool tail abc",
		"ccpool result turn1",
		"ccpool consumption abc --json",
		"ccpool capacity --json",
		"ccpool doctor",
		"ccpool version",
		"ccpool meta get abc key",
		"ccpool meta list abc --json",
	}
	for _, cmd := range sufficient {
		in := interpretForPoolTools(t, cmd)
		if !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
		}
		for _, e := range in.Effects {
			if e.Kind != EffectStdio && (e.Kind != EffectPath || e.Access != AccessRead) {
				t.Errorf("%q: want only reads and stdio, got %+v", cmd, e)
			}
		}
	}
	insufficient := []string{
		"ccpool",
		"ccpool new worker --cwd /x",
		"ccpool reply abc hello",
		"ccpool reply --help",
		"ccpool attach abc",
		"ccpool attend",
		"ccpool cancel abc",
		"ccpool close abc --purge",
		"ccpool reap",
		"ccpool reap-all",
		"ccpool emit-gauge --name x --value 1",
		"ccpool hook turn-end",
		"ccpool meta set abc k v",
		"ccpool meta rm abc k",
		"ccpool capacity --emit-metrics",
		"ccpool list --frobnicate",
		"ccpool state",
	}
	for _, cmd := range insufficient {
		if in := interpretForPoolTools(t, cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
	// trust is modeled, and states the write it performs.
	in := interpretForPoolTools(t, "ccpool trust /x/y")
	if !in.Sufficient {
		t.Fatalf("ccpool trust: want sufficient (so the write is judged), got %s", in.Insufficiency)
	}
	var wrote bool
	for _, e := range in.Effects {
		if e.Kind == EffectPath && e.Access == AccessModify && e.Path == "~/.claude.json" {
			wrote = true
		}
	}
	if !wrote {
		t.Errorf("ccpool trust: want a modify of ~/.claude.json, got %+v", in.Effects)
	}
}

// TestPgRouterDoctorRunQuery pins the doctor and run-query additions: doctor
// carries no Exec fact, run-query exactly one, and the caller-chosen core
// flags and the other process-starting verbs stay unmodeled.
func TestPgRouterDoctorRunQuery(t *testing.T) {
	execs := func(cmd string) int {
		n := 0
		for _, e := range interpretForPoolTools(t, cmd).Effects {
			if e.Kind == EffectExec {
				n++
			}
		}
		return n
	}
	for cmd, want := range map[string]int{
		"pg-router doctor":                      0,
		"pg-router doctor --clauses":            0,
		"pg-router run-query query:work":        1,
		"pg-router run-query --json query:work": 1,
		"pg-router run-query query:work --json": 1,
	} {
		if in := interpretForPoolTools(t, cmd); !in.Sufficient {
			t.Errorf("%q: want sufficient, got %s", cmd, in.Insufficiency)
		}
		if n := execs(cmd); n != want {
			t.Errorf("%q: %d exec facts, want %d", cmd, n, want)
		}
	}
	for _, cmd := range []string{
		"pg-router doctor --socket /tmp/s",
		"pg-router doctor --token x",
		"pg-router run-query",
		"pg-router run-query query:a query:b",
		"pg-router run-role worker {}",
		"pg-router gate --help",
	} {
		if in := interpretForPoolTools(t, cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
}

// TestPbUnstickInterpretation pins the pb unstick additions: every stage is an
// inert-literal command like the other pb verbs, and unlisted flags and stray
// operands stay insufficient.
func TestPbUnstickInterpretation(t *testing.T) {
	sufficient := []string{
		"pb unstick --help",
		"pb unstick prepare --help",
		"pb unstick prepare --root /ws --json",
		"pb unstick prepare --full --label agent-support --id-prefix pg2- --workdir /tmp/w",
		"pb unstick batch --workdir /tmp/w --name FOLLOWUPS --ids a,b --json",
		"pb unstick marker --outcome unchanged --reason \"still blocked\" --recheck-when on-change",
		"pb unstick marker --check --export /tmp/w/export.jsonl",
		"pb unstick report --workdir /tmp/w --root /ws --json",
	}
	for _, cmd := range sufficient {
		in := interpretForPoolTools(t, cmd)
		if !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
		}
		for _, e := range in.Effects {
			if e.Kind != EffectStdio {
				t.Errorf("%q: want no effect beyond stdio (inert model), got %+v", cmd, e)
			}
		}
	}
	for _, cmd := range []string{
		"pb unstick",
		"pb unstick frobnicate",
		"pb unstick prepare --force",
		"pb unstick prepare extra",
		"pb unstick marker --bogus x",
		"pb unstick report --workdir /tmp/w --frobnicate",
	} {
		if in := interpretForPoolTools(t, cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
}
