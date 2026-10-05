package cmddesc

import "testing"

// TestPluginProbeInterpretation pins the pg2-slsc0 schemas: every readlink /
// diff / date / command form the pb plugin (and the other agent-support
// plugins) instruct interprets as sufficient with only the effects stated, and
// the forms the schemas deliberately leave unmodeled stay insufficient so they
// abstain.
func TestPluginProbeInterpretation(t *testing.T) {
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

	// wantPaths maps a sufficient form to the PathRead targets it must emit
	// (empty: no effect beyond stdio).
	sufficient := map[string][]string{
		"readlink -f /ws/a/b.md":             {"/ws/a/b.md"},
		"readlink /ws/a/link":                {"/ws/a/link"},
		"readlink -e -n /ws/a":               {"/ws/a"},
		"readlink -f /ws/a /ws/b":            {"/ws/a", "/ws/b"},
		"diff /ws/a.md /ws/b.md":             {"/ws/a.md", "/ws/b.md"},
		"diff -u /ws/a.md /ws/b.md":          {"/ws/a.md", "/ws/b.md"},
		"diff -U 5 /ws/a.md /ws/b.md":        {"/ws/a.md", "/ws/b.md"},
		"diff --unified=5 /ws/a.md /ws/b.md": {"/ws/a.md", "/ws/b.md"},
		"diff -q -r /ws/a /ws/b":             {"/ws/a", "/ws/b"},
		"diff --from-file /ws/a /ws/b /ws/c": {"/ws/a", "/ws/b", "/ws/c"},
		"date":                               nil,
		"date +%F":                           nil,
		"date -u +%Y-%m-%dT%H:%M:%SZ":        nil,
		"date --utc +%s":                     nil,
		"date -d yesterday +%F":              nil,
		"date --date=@0 -u":                  nil,
		"date -I":                            nil,
		"date --iso-8601=seconds":            nil,
		"date -r /ws/a.md +%F":               {"/ws/a.md"},
		"date -f /ws/dates.txt":              {"/ws/dates.txt"},
		"command -v pb":                      nil,
		"command -V pb":                      nil,
	}
	for cmd, wantPaths := range sufficient {
		in := run(cmd)
		if !in.Sufficient {
			t.Errorf("%q: want sufficient, got insufficient: %s", cmd, in.Insufficiency)
			continue
		}
		var got []string
		for _, e := range in.Effects {
			switch e.Kind {
			case EffectStdio:
			case EffectPath:
				if e.Access != AccessRead {
					t.Errorf("%q: path effect %q has access %v, want read", cmd, e.Path, e.Access)
				}
				got = append(got, e.Path)
			default:
				t.Errorf("%q: unexpected effect %+v (the read-only probes model only path reads and stdout)", cmd, e)
			}
		}
		if len(got) != len(wantPaths) {
			t.Errorf("%q: path reads = %v, want %v", cmd, got, wantPaths)
			continue
		}
		for i := range got {
			if got[i] != wantPaths[i] {
				t.Errorf("%q: path reads = %v, want %v", cmd, got, wantPaths)
				break
			}
		}
	}

	insufficient := []string{
		// `command NAME ARGS` (no -v/-V) RUNS NAME: cmdparse unwraps it to a leaf for NAME,
		// which is judged as NAME itself (so `command pb drain isolate` is judged as
		// `pb drain isolate`) and never reaches this schema. Only the -v/-V lookup forms
		// are modeled, and anything else with -v/-V stays insufficient.
		"command -p -v pb",
		"command -v",
		"command -v pb extra",
		// date -s / the positional MMDDhhmm are the clock-setting spellings; -s is
		// unmodeled.
		"date -s 2026-10-05",
		"date --set=2026-10-05",
		"date --frobnicate",
		// readlink/diff flags and operands outside the instructed shapes.
		"readlink --frobnicate /ws/a",
		"diff --paginate /ws/a /ws/b",
		"diff -l /ws/a /ws/b",
		"diff --line-format=%L /ws/a /ws/b",
		"diff --palette=ad=1 /ws/a /ws/b",
		"diff -D FOO /ws/a /ws/b",
	}
	for _, cmd := range insufficient {
		if in := run(cmd); in.Sufficient {
			t.Errorf("%q: want insufficient (abstain), got sufficient", cmd)
		}
	}
}
