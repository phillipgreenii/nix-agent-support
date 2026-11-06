package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
)

// stubBins puts executable stubs on a temporary PATH.
func stubBins(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

var builtinProviderBins = []string{
	"pg-review-provider-ccaudit", "pg-review-provider-hook-decisions", "pg-review-provider-config-inventory",
}

func promptFS() fstest.MapFS {
	return fstest.MapFS{
		"prompts/base.md":           {Data: []byte("b")},
		"prompts/agent-workflow.md": {Data: []byte("a")},
		"prompts/config-health.md":  {Data: []byte("c")},
	}
}

// hermetic points every XDG variable at fresh temp dirs and writes cfg (when
// non-empty) as config.toml.
func hermetic(t *testing.T, cfg string) Env {
	t.Helper()
	root := t.TempDir()
	env := Env{ConfigHome: filepath.Join(root, "cfg"), StateHome: filepath.Join(root, "state"), CacheHome: filepath.Join(root, "cache")}
	if cfg != "" {
		if err := os.MkdirAll(env.Dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(env.Path(), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

func load(t *testing.T, cfg string) *Effective {
	t.Helper()
	e, err := Load(hermetic(t, cfg), promptFS())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMissingConfigUsesBuiltins(t *testing.T) {
	stubBins(t, builtinProviderBins...)
	e := load(t, "")
	if e.ConfigPresent {
		t.Error("ConfigPresent")
	}
	if probs := e.Validate(); len(probs) != 0 {
		t.Fatalf("problems: %v", probs)
	}
	w := e.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "no scope") {
		t.Errorf("warnings = %v", w)
	}
	want := map[string]struct {
		profile string
		provs   []string
	}{
		"agent-workflow": {"deep-review", []string{"ccaudit", "hook-decisions"}},
		"config-health":  {"health-check", []string{"config-inventory"}},
	}
	for id, w := range want {
		l, ok := e.Lens(id)
		if !ok {
			t.Fatalf("lens %s missing", id)
		}
		if l.Profile != w.profile || !reflect.DeepEqual(l.Providers, w.provs) {
			t.Errorf("%s = %+v", id, l)
		}
		if _, ok := e.Profile(l.Profile); !ok {
			t.Errorf("%s profile unresolved", id)
		}
		for _, p := range l.Providers {
			pr, ok := e.Provider(p)
			if !ok || !reflect.DeepEqual(pr.Command, []string{"pg-review-provider-" + p}) {
				t.Errorf("provider %s = %+v", p, pr)
			}
		}
	}
	if e.Limits.ItemsAutoDeferDays != 30 || e.Limits.InteractiveTTLMinutes != 240 {
		t.Errorf("limits = %+v", e.Limits)
	}
	if e.Ccpool.PoolDir != "" || !reflect.DeepEqual(e.Ccpool.ReadTools, []string{"Read", "Grep", "Glob"}) {
		t.Errorf("ccpool = %+v", e.Ccpool)
	}
	if !strings.HasSuffix(e.Ccpool.NeutralDir, filepath.Join("pg-review", "analyst-cwd")) || !strings.HasPrefix(e.Ccpool.NeutralDir, e.Env.StateHome) {
		t.Errorf("neutral dir = %q", e.Ccpool.NeutralDir)
	}
	if a := ArgsStyle(mustProvider(t, e, "config-inventory")); a != "inventory" {
		t.Errorf("args style %q", a)
	}
	if a := ArgsStyle(Provider{Name: "deployment-added"}); a != "cwd-prefix" {
		t.Errorf("args style %q", a)
	}
}

func mustProvider(t *testing.T, e *Effective, n string) Provider {
	t.Helper()
	p, ok := e.Provider(n)
	if !ok {
		t.Fatalf("provider %s", n)
	}
	return p
}

func TestBuiltinOverrideMergesKeyByKey(t *testing.T) {
	stubBins(t, builtinProviderBins...)
	e := load(t, `
[profile.deep-review]
model = "other-model"

[[provider]]
name = "ccaudit"
command = ["/somewhere/else"]

[[lens]]
id = "config-health"
kinds = ["only-one"]
`)
	dr, _ := e.Profile("deep-review")
	if dr.Model != "other-model" || dr.Minutes != 45 || dr.QueriesMax != 40 || dr.Effort != "high" {
		t.Errorf("profile = %+v", dr)
	}
	cc := mustProvider(t, e, "ccaudit")
	if !reflect.DeepEqual(cc.Command, []string{"/somewhere/else"}) {
		t.Errorf("command = %v", cc.Command)
	}
	if len(cc.Queries["agent-workflow"]) != 7 || cc.TimeoutSeconds != 120 {
		t.Errorf("queries not kept: %+v", cc)
	}
	l, _ := e.Lens("config-health")
	if !reflect.DeepEqual(l.Kinds, []string{"only-one"}) {
		t.Errorf("array not replaced whole: %v", l.Kinds)
	}
	if l.Profile != "health-check" || l.Prompt != "prompts/config-health.md" || l.MaxFindings != 8 {
		t.Errorf("other keys not kept: %+v", l)
	}
	// a provider's queries table is replaced whole
	e = load(t, "[[provider]]\nname = \"ccaudit\"\n[provider.queries]\nother = [\"q\"]\n")
	cc = mustProvider(t, e, "ccaudit")
	if len(cc.Queries) != 1 || len(cc.Queries["other"]) != 1 {
		t.Errorf("queries = %v", cc.Queries)
	}
	if len(e.Lenses) != 2 {
		t.Errorf("override must not add a lens: %d", len(e.Lenses))
	}
}

func TestHandWrittenFileLoadsWithDefaults(t *testing.T) {
	stubBins(t, builtinProviderBins...)
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "config", "valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	e := load(t, string(data))
	if probs := e.Validate(); len(probs) != 0 {
		t.Fatalf("problems: %v", probs)
	}
	if e.Limits.ItemsAutoDeferDays != 30 || e.Limits.InteractiveTTLMinutes != 240 || e.Limits.ItemsMaxPerRun != 3 {
		t.Errorf("limits = %+v", e.Limits)
	}
	if len(e.Warnings()) != 0 {
		t.Errorf("warnings = %v", e.Warnings())
	}
	s, ok := e.Scope("workspace")
	if !ok || len(s.Prefixes) != 2 {
		t.Errorf("scope = %+v", s)
	}
	hc, _ := e.Profile("health-check")
	if hc.Model != "model-small" {
		t.Errorf("profile = %+v", hc)
	}
}

func TestMaxFindingsIsTheLowerOfLensAndLimit(t *testing.T) {
	e := load(t, "[limits]\nitems_max_per_run = 6\n")
	cases := []struct{ lens, want int }{{5, 5}, {8, 6}, {0, 6}}
	for _, c := range cases {
		if got := e.MaxFindings(Lens{MaxFindings: c.lens}); got != c.want {
			t.Errorf("lens %d: got %d want %d", c.lens, got, c.want)
		}
	}
	l, _ := e.Lens("config-health") // built-in 8, limit 6
	if got := e.MaxFindings(l); got != 6 {
		t.Errorf("got %d", got)
	}
}

func TestPromptPathRelativeToConfigDir(t *testing.T) {
	stubBins(t, builtinProviderBins...)
	env := hermetic(t, `
[[lens]]
id = "mine"
profile = "health-check"
prompt = "prompts/mine.md"
providers = ["config-inventory"]
kinds = ["k"]
`)
	e, err := Load(env, promptFS())
	if err != nil {
		t.Fatal(err)
	}
	if probs := e.Validate(); len(probs) != 1 || probs[0].Table != "lens" || probs[0].ID != "mine" {
		t.Fatalf("want one missing-prompt problem, got %v", probs)
	}
	if err := os.MkdirAll(filepath.Join(env.Dir(), "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Dir(), "prompts", "mine.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if probs := e.Validate(); len(probs) != 0 {
		t.Fatalf("problems: %v", probs)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, cfg := range map[string]string{
		"malformed":   "this is [not toml",
		"bad version": "schema_version = 2\n",
	} {
		if _, err := Load(hermetic(t, cfg), promptFS()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestEnvFromOS(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "/s")
	t.Setenv("XDG_CACHE_HOME", "")
	e := EnvFromOS()
	if e.ConfigHome != "/h/.config" || e.StateHome != "/s" || e.CacheHome != "/h/.cache" {
		t.Errorf("%+v", e)
	}
}

func TestValidateRejections(t *testing.T) {
	stubBins(t, builtinProviderBins...)
	cases := []struct {
		name, cfg, want string
	}{
		{"unknown provider", "[[lens]]\nid=\"x\"\nprofile=\"deep-review\"\nprompt=\"prompts/base.md\"\nproviders=[\"nope\"]\nkinds=[\"k\"]\n", `lens "x": unknown provider "nope"`},
		{"unknown profile", "[[lens]]\nid=\"x\"\nprofile=\"nope\"\nprompt=\"prompts/base.md\"\nproviders=[\"ccaudit\"]\nkinds=[\"k\"]\n", `lens "x": unknown profile "nope"`},
		{"empty kinds", "[[lens]]\nid=\"agent-workflow\"\nkinds=[]\n", `lens "agent-workflow": empty kind list`},
		{"missing command", "[[provider]]\nname=\"newp\"\n", `provider "newp": command is missing`},
		{"command not found", "[[provider]]\nname=\"ccaudit\"\ncommand=[\"does-not-exist-anywhere\"]\n", `provider "ccaudit": command "does-not-exist-anywhere" not found`},
		{"scope without prefix", "[[scope]]\nname=\"s\"\n", `scope "s": no prefix`},
		{"duplicate lens", "[[lens]]\nid=\"agent-workflow\"\n[[lens]]\nid=\"agent-workflow\"\n", `lens "agent-workflow": duplicate id`},
		{"duplicate provider", "[[provider]]\nname=\"ccaudit\"\n[[provider]]\nname=\"ccaudit\"\n", `provider "ccaudit": duplicate id`},
		{"duplicate scope", "[[scope]]\nname=\"s\"\nprefixes=[\"/a\"]\n[[scope]]\nname=\"s\"\nprefixes=[\"/b\"]\n", `scope "s": duplicate id`},
		{"missing prompt", "[[lens]]\nid=\"agent-workflow\"\nprompt=\"prompts/gone.md\"\n", `lens "agent-workflow": prompt file "prompts/gone.md" does not exist`},
		{"missing base prompt", "[[lens]]\nid=\"agent-workflow\"\nbase_prompt=\"prompts/gone.md\"\n", `base_prompt file "prompts/gone.md"`},
		{"profile without model", "[profile.fresh]\neffort=\"low\"\n", `profile "fresh": no model`},
		{"bad effort", "[profile.deep-review]\neffort=\"extreme\"\n", `profile "deep-review": effort "extreme"`},
		{"bad args style", "[[provider]]\nname=\"ccaudit\"\nargs_style=\"zzz\"\n", `provider "ccaudit": args_style "zzz"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := load(t, c.cfg)
			var lines []string
			for _, p := range e.Validate() {
				lines = append(lines, p.String())
			}
			if !strings.Contains(strings.Join(lines, "\n"), c.want) {
				t.Errorf("want %q in:\n%s", c.want, strings.Join(lines, "\n"))
			}
		})
	}
}

func TestMissingBuiltinProviderCommandIsReported(t *testing.T) {
	stubBins(t, "pg-review-provider-ccaudit", "pg-review-provider-hook-decisions")
	e := load(t, "")
	probs := e.Validate()
	if len(probs) != 1 || probs[0].String() != `provider "config-inventory": command "pg-review-provider-config-inventory" not found: not an executable file and not on PATH` {
		t.Fatalf("%v", probs)
	}
}

func TestBuiltinFSIsEmbedded(t *testing.T) {
	if _, err := builtin.FS().Open(".gitkeep"); err != nil {
		t.Fatal(err)
	}
}
