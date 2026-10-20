package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// presetsOf loads body and parses its generators and reports sections.
func presetsOf(t *testing.T, body string) PresetSet {
	t.Helper()
	isolateEnv(t)
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return Presets(c)
}

func problemFor(s PresetSet, kind, generator, preset string) *Problem {
	for i := range s.Problems {
		p := &s.Problems[i]
		if p.Kind == kind && p.Generator == generator && p.Preset == preset {
			return p
		}
	}
	return nil
}

func TestPresetsEmptyConfigHasBuiltinBaseline(t *testing.T) {
	_, st := isolateEnv(t)
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	s := Presets(c)
	if len(s.Problems) != 0 {
		t.Fatalf("problems = %+v", s.Problems)
	}
	g, ok := s.Generators["baseline"]
	if !ok || !g.Builtin || len(s.Generators) != 1 {
		t.Errorf("generators = %+v", s.Generators)
	}
	p, ok := s.Presets["baseline"]
	if !ok || !p.Builtin || p.Generator != "baseline" || p.Range != "today" || len(s.Presets) != 1 {
		t.Errorf("presets = %+v", s.Presets)
	}
	if want := st + "/work-report/reports/baseline"; p.OutputDir != want {
		t.Errorf("baseline output dir = %q, want %q", p.OutputDir, want)
	}
}

func TestPresetsValidSections(t *testing.T) {
	_, st := isolateEnv(t)
	t.Setenv("MYDIR", "/mnt-x/out")
	c, err := Load(writeConfig(t, `generators:
  narrative:
    command: [/bin/work-report-narrative]
    timeout: 10m
    inputs: [baseline]
    env: [ANTHROPIC_API_KEY, HTTPS_PROXY]
  json-dump:
    command: [work-report-json-dump]
reports:
  example-daily:
    generator: narrative
    range: yesterday
    narrowing: { labels: [a, b], sources: [s], types: [] }
    config:
      model: sonnet
      nested: {list: [1, {x: y}]}
    output:
      dir: $MYDIR/daily
  example-weekly:
    generator: json-dump
  from-baseline:
    generator: baseline
    range: last-week
`))
	if err != nil {
		t.Fatal(err)
	}
	s := Presets(c)
	if len(s.Problems) != 0 {
		t.Fatalf("problems = %+v", s.Problems)
	}
	n := s.Generators["narrative"]
	if !reflect.DeepEqual(n.Command, []string{"/bin/work-report-narrative"}) || n.Timeout != 10*time.Minute ||
		!reflect.DeepEqual(n.Inputs, []string{"baseline"}) || !reflect.DeepEqual(n.Env, []string{"ANTHROPIC_API_KEY", "HTTPS_PROXY"}) {
		t.Errorf("narrative = %+v", n)
	}
	if j := s.Generators["json-dump"]; j.Timeout != DefaultGeneratorTimeout || len(j.Inputs) != 0 || len(j.Env) != 0 {
		t.Errorf("json-dump defaults = %+v", j)
	}
	d := s.Presets["example-daily"]
	if d.Generator != "narrative" || d.Range != "yesterday" || d.OutputDir != "/mnt-x/out/daily" ||
		!reflect.DeepEqual(d.Narrowing, Narrowing{Labels: []string{"a", "b"}, Sources: []string{"s"}, Types: []string{}}) {
		t.Errorf("example-daily = %+v", d)
	}
	wantCfg := map[string]any{"model": "sonnet", "nested": map[string]any{"list": []any{1, map[string]any{"x": "y"}}}}
	if !reflect.DeepEqual(d.Config, wantCfg) {
		t.Errorf("config = %#v, want %#v", d.Config, wantCfg)
	}
	w := s.Presets["example-weekly"]
	if w.Range != "today" || w.OutputDir != st+"/work-report/reports/example-weekly" || len(w.Narrowing.Labels) != 0 {
		t.Errorf("example-weekly defaults = %+v", w)
	}
	if fb := s.Presets["from-baseline"]; fb.Generator != "baseline" || fb.Range != "last-week" || fb.Config == nil {
		t.Errorf("from-baseline = %+v", fb)
	}
}

func TestPresetsEveryCheck(t *testing.T) {
	isolateEnv(t)
	t.Setenv("HOME", "/home-x")
	for _, tc := range []struct {
		name, body, kind, generator, preset string
	}{
		{"generator bad name", "generators:\n  Bad_Name:\n    command: [x]\n", ProblemBadName, "Bad_Name", ""},
		{"generator name with slash", "generators:\n  a/b:\n    command: [x]\n", ProblemBadName, "a/b", ""},
		{"generator leading dash", "generators:\n  -a:\n    command: [x]\n", ProblemBadName, "-a", ""},
		{"preset bad name", "reports:\n  Up:\n    generator: baseline\n", ProblemBadName, "", "Up"},
		{"redefined baseline generator", "generators:\n  baseline:\n    command: [x]\n", ProblemReservedName, "baseline", ""},
		{"redefined baseline preset", "reports:\n  baseline:\n    generator: baseline\n", ProblemReservedName, "", "baseline"},
		{"missing command", "generators:\n  g: {timeout: 1m}\n", ProblemBadCommand, "g", ""},
		{"empty command", "generators:\n  g: {command: []}\n", ProblemBadCommand, "g", ""},
		{"empty first element", "generators:\n  g: {command: ['']}\n", ProblemBadCommand, "g", ""},
		{"command not a list", "generators:\n  g: {command: x}\n", ProblemBadCommand, "g", ""},
		{"command number", "generators:\n  g: {command: [5]}\n", ProblemBadCommand, "g", ""},
		{"generator unknown key", "generators:\n  g: {command: [x], commnd: [y]}\n", ProblemBadField, "g", ""},
		{"generator not a mapping", "generators:\n  g: [x]\n", ProblemBadField, "g", ""},
		{"bad timeout", "generators:\n  g: {command: [x], timeout: soon}\n", ProblemBadField, "g", ""},
		{"zero timeout", "generators:\n  g: {command: [x], timeout: 0s}\n", ProblemBadField, "g", ""},
		{"env not names", "generators:\n  g: {command: [x], env: ['A=B']}\n", ProblemBadField, "g", ""},
		{"inputs other than baseline", "generators:\n  g: {command: [x], inputs: [store]}\n", ProblemBadInputs, "g", ""},
		{"inputs wrong type", "generators:\n  g: {command: [x], inputs: baseline}\n", ProblemBadInputs, "g", ""},
		{"unknown generator", "reports:\n  p: {generator: nope}\n", ProblemUnknownGenerator, "", "p"},
		{"unknown generator via bad entry", "generators:\n  g: {}\nreports:\n  p: {generator: g}\n", ProblemUnknownGenerator, "", "p"},
		{"preset without generator", "reports:\n  p: {range: today}\n", ProblemBadField, "", "p"},
		{"bad range", "reports:\n  p: {generator: baseline, range: fortnight}\n", ProblemBadRange, "", "p"},
		{"empty range", "reports:\n  p: {generator: baseline, range: ''}\n", ProblemBadRange, "", "p"},
		{"config on baseline generator", "reports:\n  p: {generator: baseline, config: {a: 1}}\n", ProblemBadField, "", "p"},
		{"config not a mapping", "generators:\n  g: {command: [x]}\nreports:\n  p: {generator: g, config: [1]}\n", ProblemBadField, "", "p"},
		{"preset unknown key", "reports:\n  p: {generator: baseline, rnge: today}\n", ProblemBadField, "", "p"},
		{"narrowing wrong type", "reports:\n  p: {generator: baseline, narrowing: {labels: a}}\n", ProblemBadField, "", "p"},
		{"narrowing unknown dimension", "reports:\n  p: {generator: baseline, narrowing: {colors: [a]}}\n", ProblemBadField, "", "p"},
		{"output unknown key", "reports:\n  p: {generator: baseline, output: {path: /a}}\n", ProblemBadField, "", "p"},
		{"output.dir relative", "reports:\n  p: {generator: baseline, output: {dir: rel/x}}\n", ProblemBadOutputDir, "", "p"},
		{"output.dir unset var", "reports:\n  p: {generator: baseline, output: {dir: '$NOPE_UNSET_VAR'}}\n", ProblemBadOutputDir, "", "p"},
		{"output.dir unset var in path", "reports:\n  p: {generator: baseline, output: {dir: '$NOPE_UNSET_VAR/x'}}\n", ProblemBadOutputDir, "", "p"},
		{"output.dir tilde user", "reports:\n  p: {generator: baseline, output: {dir: '~bob/x'}}\n", ProblemBadOutputDir, "", "p"},
		{"output.dir empty", "reports:\n  p: {generator: baseline, output: {dir: ''}}\n", ProblemBadOutputDir, "", "p"},
		{"output.dir not a string", "reports:\n  p: {generator: baseline, output: {dir: 5}}\n", ProblemBadOutputDir, "", "p"},
		{"generators not a mapping", "generators: [a]\n", ProblemBadField, "", ""},
		{"reports not a mapping", "reports: hello\n", ProblemBadField, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := presetsOf(t, tc.body)
			if problemFor(s, tc.kind, tc.generator, tc.preset) == nil {
				t.Fatalf("want a %s problem for generator=%q preset=%q, got %+v", tc.kind, tc.generator, tc.preset, s.Problems)
			}
			if _, ok := s.Presets["baseline"]; !ok {
				t.Error("built-in baseline preset must survive a problem")
			}
			if _, ok := s.Generators["baseline"]; !ok {
				t.Error("built-in baseline generator must survive a problem")
			}
			if tc.kind == ProblemReservedName && (!s.Presets["baseline"].Builtin || !s.Generators["baseline"].Builtin) {
				t.Error("a redefinition must not replace the built-in baseline")
			}
			if tc.preset != "" && tc.kind != ProblemReservedName {
				if _, ok := s.Presets[tc.preset]; ok {
					t.Errorf("invalid preset %q must not be returned", tc.preset)
				}
			}
			if tc.generator != "" {
				if _, ok := s.Generators[tc.generator]; ok && tc.kind != ProblemReservedName {
					t.Errorf("invalid generator %q must not be returned", tc.generator)
				}
			}
		})
	}
}

func TestPresetsKeepsValidEntriesAndAttributesProblems(t *testing.T) {
	s := presetsOf(t, `generators:
  good: {command: [x]}
  broken: {command: []}
  Bad: {command: [x]}
reports:
  fine: {generator: good, range: week}
  wrong-range: {generator: good, range: nope}
  dangling: {generator: missing}
  uses-broken: {generator: broken}
`)
	if _, ok := s.Generators["good"]; !ok {
		t.Error("valid generator dropped")
	}
	if _, ok := s.Presets["fine"]; !ok {
		t.Error("valid preset dropped")
	}
	for _, name := range []string{"broken", "Bad"} {
		if _, ok := s.Generators[name]; ok {
			t.Errorf("invalid generator %q returned", name)
		}
	}
	for _, name := range []string{"wrong-range", "dangling", "uses-broken"} {
		if _, ok := s.Presets[name]; ok {
			t.Errorf("invalid preset %q returned", name)
		}
	}
	for _, want := range []struct{ kind, g, p string }{
		{ProblemBadCommand, "broken", ""},
		{ProblemBadName, "Bad", ""},
		{ProblemBadRange, "", "wrong-range"},
		{ProblemUnknownGenerator, "", "dangling"},
		{ProblemUnknownGenerator, "", "uses-broken"},
	} {
		if problemFor(s, want.kind, want.g, want.p) == nil {
			t.Errorf("missing %+v in %+v", want, s.Problems)
		}
	}
	if len(s.Problems) != 5 {
		t.Errorf("problems = %+v, want exactly 5", s.Problems)
	}
	for _, p := range s.Problems {
		if p.Message == "" {
			t.Errorf("problem %+v has no message", p)
		}
	}
}

func TestPresetsOutputDirExpansion(t *testing.T) {
	_, st := isolateEnv(t)
	t.Setenv("HOME", "/home-x")
	t.Setenv("BASE", "/base")
	for _, tc := range []struct{ in, want string }{
		{"~", "/home-x"},
		{"~/reports/a", "/home-x/reports/a"},
		{"$BASE/r", "/base/r"},
		{"${BASE}/r/", "/base/r"},
		{"$XDG_STATE_HOME/custom", st + "/custom"},
		{"/abs/../abs2", "/abs2"},
	} {
		got, err := ExpandOutputDir(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ExpandOutputDir(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	// $XDG_STATE_HOME unset reuses the loader's fallback under HOME.
	t.Setenv("XDG_STATE_HOME", "")
	if got, err := ExpandOutputDir("$XDG_STATE_HOME/x"); err != nil || got != "/home-x/.local/state/x" {
		t.Errorf("XDG fallback = %q, %v", got, err)
	}
	for _, in := range []string{"", "rel", "./rel", "~bob", "~bob/x", "$NOPE_UNSET_VAR", "$NOPE_UNSET_VAR/x"} {
		if got, err := ExpandOutputDir(in); err == nil {
			t.Errorf("ExpandOutputDir(%q) = %q, want an error", in, got)
		}
	}
	t.Setenv("HOME", "")
	if _, err := ExpandOutputDir("~/x"); err == nil {
		t.Error("~ with HOME unset must be an error")
	}
}

func TestPresetsMissingRangeDefaultsToToday(t *testing.T) {
	s := presetsOf(t, "generators:\n  g: {command: [x]}\nreports:\n  p: {generator: g}\n")
	if p := s.Presets["p"]; p.Range != "today" {
		t.Errorf("range = %q, want today", p.Range)
	}
}

func TestRedact(t *testing.T) {
	in := map[string]any{
		"model":        "sonnet",
		"apiKey":       "abc",
		"monkey":       map[string]any{"a": 1},
		"maxTokens":    []any{1, 2},
		"SECRET_THING": "s",
		"Password":     "p",
		"credentials":  map[string]any{"a": 1},
		"nested": map[string]any{
			"ok":       "v",
			"sortKey":  "x",
			"deeper":   []any{map[string]any{"token": "t", "keep": "k"}},
			"fine-two": map[string]any{"auth_token": "t"},
		},
	}
	got := Redact(in)
	want := map[string]any{
		"model":        "sonnet",
		"apiKey":       "***",
		"monkey":       "***",
		"maxTokens":    "***",
		"SECRET_THING": "***",
		"Password":     "***",
		"credentials":  "***",
		"nested": map[string]any{
			"ok":       "v",
			"sortKey":  "***",
			"deeper":   []any{map[string]any{"token": "***", "keep": "k"}},
			"fine-two": map[string]any{"auth_token": "***"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Redact = %#v\nwant %#v", got, want)
	}
	if in["apiKey"] != "abc" || in["nested"].(map[string]any)["sortKey"] != "x" {
		t.Error("Redact must not modify its input")
	}
	if r := Redact(nil); r == nil || len(r) != 0 {
		t.Errorf("Redact(nil) = %#v, want empty map", r)
	}
}

func TestKindsMigrationTextNamesTheTranslation(t *testing.T) {
	for _, want := range []string{"--kind was removed", "work-report report <preset>", "kinds.narrative.model becoming reports.<name>.config.model", "kinds.narrative.systemPromptFile becoming reports.<name>.config.promptFile", "injection guard is fixed"} {
		if !strings.Contains(KindsMigrationText, want) {
			t.Errorf("migration text lacks %q", want)
		}
	}
}
