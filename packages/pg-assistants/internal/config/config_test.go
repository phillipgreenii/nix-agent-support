package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pathsTOML is a complete, valid [paths] table.
const pathsTOML = `[paths]
tmux = "/example/store/bin/tmux"
claude = "/example/store/bin/claude"
pg_claude_control = "/example/store/bin/pg-claude-control"
shell = "/bin/zsh"
state_dir = "/example/state/pg-assistants"
claude_json = "/example/home/.claude.json"
`

// assistant renders one complete, valid [[assistant]] entry for id. Each
// override is a `key = value` line (dotted keys such as `context.kill` reach
// the context table) that replaces the line for the same key, or adds it.
func assistant(id string, overrides ...string) string {
	type kv struct{ k, v string }
	fields := []kv{
		{"id", `"` + id + `"`},
		{"name", `"Example ` + id + `"`},
		{"model", `"claude-sonnet-5-5"`},
		{"effort", `"medium"`},
		{"cwd", `"/example/work/` + id + `"`},
		{"prompt", `"Resume your duties per AGENTS.md."`},
		{"context.limit", "200000"},
		{"context.nudge", "0.80"},
		{"context.interrupt", "0.95"},
	}
	for _, o := range overrides {
		k, v, ok := strings.Cut(o, " = ")
		if !ok {
			panic("override must be `key = value`: " + o)
		}
		i := slices.IndexFunc(fields, func(f kv) bool { return f.k == k })
		if i < 0 {
			fields = append(fields, kv{k, v})
			continue
		}
		fields[i].v = v
	}
	var b strings.Builder
	b.WriteString("\n[[assistant]]\n")
	for _, f := range fields {
		b.WriteString(f.k + " = " + f.v + "\n")
	}
	return b.String()
}

// writeConfig writes body to config.toml in a fresh temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// load writes body and loads it, failing the test on a Load error.
func load(t *testing.T, body string) Config {
	t.Helper()
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v\n--- config ---\n%s", err, body)
	}
	return cfg
}

func fields(ps []FieldError) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Field)
	}
	return out
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg := load(t, pathsTOML+assistant("example-a"))

	if cfg.Tick != time.Minute {
		t.Errorf("Tick = %v, want 1m", cfg.Tick)
	}
	if want := (Grace{Interrupt: 5 * time.Minute, Kill: 2 * time.Minute}); cfg.Grace != want {
		t.Errorf("Grace = %+v, want %+v", cfg.Grace, want)
	}
	wantBlocks := Blocks{Points: Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.00}, UnknownResetHold: time.Hour}
	if cfg.Blocks != wantBlocks {
		t.Errorf("Blocks = %+v, want %+v", cfg.Blocks, wantBlocks)
	}
	wantRestart := Restart{MinUptime: time.Minute, BackoffInitial: time.Minute, BackoffMax: 30 * time.Minute, StartTimeout: 5 * time.Minute}
	if cfg.Restart != wantRestart {
		t.Errorf("Restart = %+v, want %+v", cfg.Restart, wantRestart)
	}
	if len(cfg.Problems) != 0 {
		t.Errorf("Problems = %v, want none", cfg.Problems)
	}
	if len(cfg.Assistants) != 1 {
		t.Fatalf("got %d assistants, want 1", len(cfg.Assistants))
	}
	a := cfg.Assistants[0]
	if a.Tick != time.Minute {
		t.Errorf("Assistants[0].Tick = %v, want 1m", a.Tick)
	}
	if a.Context.Kill != 1.00 {
		t.Errorf("Assistants[0].Context.Kill = %v, want 1.00", a.Context.Kill)
	}
	if !a.Enabled {
		t.Error("Assistants[0].Enabled = false, want true when the key is absent")
	}
	if a.Context.Window != 1_000_000 {
		t.Errorf("Assistants[0].Context.Window = %d, want 1000000 (claude-sonnet-5-5 from the table)", a.Context.Window)
	}
	if len(a.Problems) != 0 {
		t.Errorf("Assistants[0].Problems = %v, want none", a.Problems)
	}
}

func TestLoadReadsEveryKey(t *testing.T) {
	body := `tick = "2m"
[grace]
interrupt = "6m"
kill = "3m"
[blocks]
nudge = 0.90
interrupt = 0.96
kill = 0.99
unknown_reset_hold = "2h"
[restart]
min_uptime = "90s"
backoff_initial = "2m"
backoff_max = "1h"
start_timeout = "4m"
` + pathsTOML + assistant(
		"example-a",
		`name = "Inbox Keeper"`,
		`model = "claude-opus-5-5"`,
		`effort = "high"`,
		`cwd = "/example/work/inbox"`,
		`prompt = "Resume your duties."`,
		`enabled = false`,
		`tick = "30s"`,
		`env = { EXAMPLE_FOO = "bar", EXAMPLE_BAZ = "qux" }`,
		`context.limit = 300000`,
		`context.window = 900000`,
		`context.nudge = 0.7`,
		`context.interrupt = 0.9`,
		`context.kill = 1`,
	)
	cfg := load(t, body)

	want := Config{
		Tick:    2 * time.Minute,
		Grace:   Grace{Interrupt: 6 * time.Minute, Kill: 3 * time.Minute},
		Blocks:  Blocks{Points: Points{Nudge: 0.90, Interrupt: 0.96, Kill: 0.99}, UnknownResetHold: 2 * time.Hour},
		Restart: Restart{MinUptime: 90 * time.Second, BackoffInitial: 2 * time.Minute, BackoffMax: time.Hour, StartTimeout: 4 * time.Minute},
		Paths: Paths{
			Tmux:            "/example/store/bin/tmux",
			Claude:          "/example/store/bin/claude",
			PgClaudeControl: "/example/store/bin/pg-claude-control",
			Shell:           "/bin/zsh",
			StateDir:        "/example/state/pg-assistants",
			ClaudeJSON:      "/example/home/.claude.json",
		},
	}
	if cfg.Tick != want.Tick || cfg.Grace != want.Grace || cfg.Blocks != want.Blocks || cfg.Restart != want.Restart || cfg.Paths != want.Paths {
		t.Errorf("globals = %+v %+v %+v %+v %+v\nwant      %+v %+v %+v %+v %+v",
			cfg.Tick, cfg.Grace, cfg.Blocks, cfg.Restart, cfg.Paths,
			want.Tick, want.Grace, want.Blocks, want.Restart, want.Paths)
	}
	if len(cfg.Problems) != 0 {
		t.Errorf("Problems = %v, want none", cfg.Problems)
	}
	if len(cfg.Assistants) != 1 {
		t.Fatalf("got %d assistants, want 1", len(cfg.Assistants))
	}
	a := cfg.Assistants[0]
	if a.ID != "example-a" || a.Name != "Inbox Keeper" || a.Model != "claude-opus-5-5" || a.Effort != "high" ||
		a.Cwd != "/example/work/inbox" || a.Prompt != "Resume your duties." || a.Enabled || a.Tick != 30*time.Second {
		t.Errorf("assistant = %+v", a)
	}
	if want := map[string]string{"EXAMPLE_FOO": "bar", "EXAMPLE_BAZ": "qux"}; len(a.Env) != len(want) || a.Env["EXAMPLE_FOO"] != "bar" || a.Env["EXAMPLE_BAZ"] != "qux" {
		t.Errorf("Env = %v, want %v", a.Env, want)
	}
	wantCtx := Context{Limit: 300000, Window: 900000, Points: Points{Nudge: 0.7, Interrupt: 0.9, Kill: 1}}
	if a.Context != wantCtx {
		t.Errorf("Context = %+v, want %+v", a.Context, wantCtx)
	}
	if len(a.Problems) != 0 {
		t.Errorf("Problems = %v, want none", a.Problems)
	}
}

func TestPerAssistantTickOverrides(t *testing.T) {
	cfg := load(t, pathsTOML+assistant("example-a", `tick = "30s"`)+assistant("example-b"))
	if got := cfg.Assistants[0].Tick; got != 30*time.Second {
		t.Errorf("Assistants[0].Tick = %v, want 30s", got)
	}
	if got := cfg.Assistants[1].Tick; got != time.Minute {
		t.Errorf("Assistants[1].Tick = %v, want the global 1m", got)
	}
}

func TestAssistantRules(t *testing.T) {
	cases := []struct {
		name       string
		assistants []string
		want       map[int]string // assistant index -> a Field its Problems MUST hold
	}{
		{
			"duplicate id marks both",
			[]string{assistant("example-a"), assistant("example-a", `cwd = "/example/work/other"`)},
			map[int]string{0: "assistant[example-a].id", 1: "assistant[example-a].id"},
		},
		{"id outside [a-z0-9-]+", []string{assistant("Bad_ID")}, map[int]string{0: "assistant[#1].id"}},
		{"relative cwd", []string{assistant("example-a", `cwd = "work/example"`)}, map[int]string{0: "assistant[example-a].cwd"}},
		{
			"shared cwd marks both",
			[]string{assistant("example-a", `cwd = "/example/work/shared"`), assistant("example-b", `cwd = "/example/work/shared/"`)},
			map[int]string{0: "assistant[example-a].cwd", 1: "assistant[example-b].cwd"},
		},
		{"empty name", []string{assistant("example-a", `name = ""`)}, map[int]string{0: "assistant[example-a].name"}},
		{"empty model", []string{assistant("example-a", `model = ""`, `context.window = 1000000`)}, map[int]string{0: "assistant[example-a].model"}},
		{"effort turbo", []string{assistant("example-a", `effort = "turbo"`)}, map[int]string{0: "assistant[example-a].effort"}},
		{"empty prompt", []string{assistant("example-a", `prompt = ""`)}, map[int]string{0: "assistant[example-a].prompt"}},
		{"prompt starting with -", []string{assistant("example-a", `prompt = "--resume"`)}, map[int]string{0: "assistant[example-a].prompt"}},
		{"limit 0", []string{assistant("example-a", `context.limit = 0`)}, map[int]string{0: "assistant[example-a].context.limit"}},
		{"context nudge 0", []string{assistant("example-a", `context.nudge = 0.0`)}, map[int]string{0: "assistant[example-a].context.nudge"}},
		{
			"nudge 0.9 >= interrupt 0.8",
			[]string{assistant("example-a", `context.nudge = 0.9`, `context.interrupt = 0.8`)},
			map[int]string{0: "assistant[example-a].context.interrupt"},
		},
		{
			"interrupt 0.95 > kill 0.9",
			[]string{assistant("example-a", `context.interrupt = 0.95`, `context.kill = 0.9`)},
			map[int]string{0: "assistant[example-a].context.kill"},
		},
		{
			"limit 200000 kill 1.05 on a 200000 window",
			[]string{assistant("example-a", `model = "claude-haiku-4-5"`, `context.limit = 200000`, `context.kill = 1.05`)},
			map[int]string{0: "assistant[example-a].context.kill"},
		},
		{
			"limit*kill exactly the window",
			[]string{assistant("example-a", `context.limit = 1000000`, `context.kill = 1.0`)},
			map[int]string{0: "assistant[example-a].context.kill"},
		},
		{
			"unknown model without context.window",
			[]string{assistant("example-a", `model = "claude-sonnet-9"`)},
			map[int]string{0: "assistant[example-a].model"},
		},
		// tmux cannot pass on a value that ends in `\;` intact.
		{"name ending in \\;", []string{assistant("example-a", `name = "Example\\;"`)}, map[int]string{0: "assistant[example-a].name"}},
		{
			"model ending in \\;",
			[]string{assistant("example-a", `model = "claude-sonnet-5-5\\;"`, `context.window = 1000000`)},
			map[int]string{0: "assistant[example-a].model"},
		},
		{"cwd ending in \\;", []string{assistant("example-a", `cwd = "/example/work/a\\;"`)}, map[int]string{0: "assistant[example-a].cwd"}},
		{"prompt ending in \\;", []string{assistant("example-a", `prompt = "Resume now\\;"`)}, map[int]string{0: "assistant[example-a].prompt"}},
		// tmux expands a format in the start directory (##, #{...}).
		{"cwd with ##", []string{assistant("example-a", `cwd = "/example/work/a##b"`)}, map[int]string{0: "assistant[example-a].cwd"}},
		{"cwd with #{", []string{assistant("example-a", `cwd = "/example/work/#{session_name}"`)}, map[int]string{0: "assistant[example-a].cwd"}},
		{"cwd with a lone #", []string{assistant("example-a", `cwd = "/example/work/a#b"`)}, map[int]string{0: "assistant[example-a].cwd"}},
		{"negative tick", []string{assistant("example-a", `tick = "-1s"`)}, map[int]string{0: "assistant[example-a].tick"}},
		{"unparseable tick", []string{assistant("example-a", `tick = "soon"`)}, map[int]string{0: "assistant[example-a].tick"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, pathsTOML+strings.Join(tc.assistants, ""))
			if len(cfg.Problems) != 0 {
				t.Errorf("global Problems = %v, want none", cfg.Problems)
			}
			if len(cfg.Assistants) != len(tc.assistants) {
				t.Fatalf("got %d assistants, want %d", len(cfg.Assistants), len(tc.assistants))
			}
			for i, a := range cfg.Assistants {
				field, marked := tc.want[i]
				if !marked {
					if len(a.Problems) != 0 {
						t.Errorf("assistant %d Problems = %v, want none", i, a.Problems)
					}
					continue
				}
				if !slices.Contains(fields(a.Problems), field) {
					t.Errorf("assistant %d Problems = %v, want one for %s", i, a.Problems, field)
				}
			}
		})
	}
}

func TestSharedProblemsNameTheOthers(t *testing.T) {
	msgOf := func(a Assistant, field string) string {
		for _, p := range a.Problems {
			if p.Field == field {
				return p.Msg
			}
		}
		return ""
	}
	dup := load(t, pathsTOML+assistant("example-a")+assistant("example-a", `cwd = "/example/work/other"`))
	if got := msgOf(dup.Assistants[0], "assistant[example-a].id"); !strings.Contains(got, "assistant #2") {
		t.Errorf("first duplicate's message = %q, want it to name assistant #2", got)
	}
	if got := msgOf(dup.Assistants[1], "assistant[example-a].id"); !strings.Contains(got, "assistant #1") {
		t.Errorf("second duplicate's message = %q, want it to name assistant #1", got)
	}
	shared := load(t, pathsTOML+assistant("example-a", `cwd = "/example/work/s"`)+assistant("example-b", `cwd = "/example/work/s"`))
	if got := msgOf(shared.Assistants[0], "assistant[example-a].cwd"); !strings.Contains(got, "assistant #2 (assistant[example-b])") {
		t.Errorf("shared cwd message = %q, want it to name assistant #2 (assistant[example-b])", got)
	}
}

func TestAssistantRulesAccept(t *testing.T) {
	cases := []struct {
		name       string
		overrides  []string
		wantWindow int64
	}{
		{"interrupt == kill", []string{`context.interrupt = 0.95`, `context.kill = 0.95`}, 1_000_000},
		{"unknown model with an explicit context.window", []string{`model = "claude-sonnet-9"`, `context.window = 500000`}, 500_000},
		{"[1m] suffix", []string{`model = "claude-sonnet-5-5[1m]"`, `context.limit = 800000`}, 1_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, pathsTOML+assistant("example-a", tc.overrides...))
			a := cfg.Assistants[0]
			if len(a.Problems) != 0 || len(cfg.Problems) != 0 {
				t.Fatalf("Problems = %v / %v, want none", a.Problems, cfg.Problems)
			}
			if a.Context.Window != tc.wantWindow {
				t.Errorf("Context.Window = %d, want %d", a.Context.Window, tc.wantWindow)
			}
		})
	}
}

func TestLoadIsolatesAssistantProblems(t *testing.T) {
	path := writeConfig(t, pathsTOML+assistant("example-a", `effort = "turbo"`)+assistant("example-b"))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v, want no error for a per-assistant problem", err)
	}
	if got := fields(cfg.Assistants[0].Problems); !slices.Contains(got, "assistant[example-a].effort") {
		t.Errorf("bad assistant Problems = %v, want assistant[example-a].effort", got)
	}
	if got := cfg.Assistants[1].Problems; len(got) != 0 {
		t.Errorf("good assistant Problems = %v, want none", got)
	}
	if len(cfg.Problems) != 0 {
		t.Errorf("global Problems = %v, want none", cfg.Problems)
	}
}

func TestGlobalProblemFallsBackToDefault(t *testing.T) {
	cases := []struct {
		name  string
		toml  string
		field string
		check func(t *testing.T, cfg Config)
	}{
		{"grace.kill 0s", "[grace]\nkill = \"0s\"\n", "grace.kill", func(t *testing.T, cfg Config) {
			if cfg.Grace.Kill != 2*time.Minute {
				t.Errorf("Grace.Kill = %v, want the default 2m", cfg.Grace.Kill)
			}
		}},
		{"grace.kill later", "[grace]\nkill = \"later\"\n", "grace.kill", func(t *testing.T, cfg Config) {
			if cfg.Grace.Kill != 2*time.Minute {
				t.Errorf("Grace.Kill = %v, want the default 2m", cfg.Grace.Kill)
			}
		}},
		{"grace.kill not a string", "[grace]\nkill = 5\n", "grace.kill", func(t *testing.T, cfg Config) {
			if cfg.Grace.Kill != 2*time.Minute {
				t.Errorf("Grace.Kill = %v, want the default 2m", cfg.Grace.Kill)
			}
		}},
		{"blocks.interrupt below nudge", "[blocks]\nnudge = 0.6\ninterrupt = 0.5\nkill = 0.9\n", "blocks.interrupt", func(t *testing.T, cfg Config) {
			if want := (Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.00}); cfg.Blocks.Points != want {
				t.Errorf("Blocks.Points = %+v, want the defaults %+v", cfg.Blocks.Points, want)
			}
		}},
		{"global tick negative", "tick = \"-5s\"\n", "tick", func(t *testing.T, cfg Config) {
			if cfg.Tick != time.Minute || cfg.Assistants[0].Tick != time.Minute {
				t.Errorf("Tick = %v, assistant tick = %v, want the default 1m for both", cfg.Tick, cfg.Assistants[0].Tick)
			}
		}},
		{"restart.backoff_max 0s", "[restart]\nbackoff_max = \"0s\"\n", "restart.backoff_max", func(t *testing.T, cfg Config) {
			if cfg.Restart.BackoffMax != 30*time.Minute {
				t.Errorf("Restart.BackoffMax = %v, want the default 30m", cfg.Restart.BackoffMax)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, tc.toml+pathsTOML+assistant("example-a"))
			if got := fields(cfg.Problems); !slices.Contains(got, tc.field) {
				t.Errorf("Problems = %v, want one for %s", cfg.Problems, tc.field)
			}
			if got := cfg.Assistants[0].Problems; len(got) != 0 {
				t.Errorf("assistant Problems = %v, want none (a global problem is not the assistant's)", got)
			}
			tc.check(t, cfg)
		})
	}
}

func TestUnknownKeysAreProblems(t *testing.T) {
	body := "tikc = \"1m\"\n[grace]\nkil = \"1m\"\n" + pathsTOML + assistant("example-a", `promt = "x"`, `context.kil = 1.0`)
	cfg := load(t, body)
	for _, f := range []string{"tikc", "grace.kil"} {
		if !slices.Contains(fields(cfg.Problems), f) {
			t.Errorf("global Problems = %v, want one for %s", cfg.Problems, f)
		}
	}
	for _, f := range []string{"assistant[example-a].promt", "assistant[example-a].context.kil"} {
		if !slices.Contains(fields(cfg.Assistants[0].Problems), f) {
			t.Errorf("assistant Problems = %v, want one for %s", cfg.Assistants[0].Problems, f)
		}
	}
}

func TestAssistantEnvRules(t *testing.T) {
	cases := []struct {
		name, env, field string
	}{
		{"value not a string", `env = { EXAMPLE_FOO = 1 }`, "assistant[example-a].env.EXAMPLE_FOO"},
		{"key not a variable name", `env = { "EXAMPLE=FOO" = "bar" }`, "assistant[example-a].env.EXAMPLE=FOO"},
		{"not a table", `env = "EXAMPLE_FOO=bar"`, "assistant[example-a].env"},
		{"value ending in \\;", `env = { EXAMPLE_FOO = "bar\\;" }`, "assistant[example-a].env.EXAMPLE_FOO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, pathsTOML+assistant("example-a", tc.env))
			if got := fields(cfg.Assistants[0].Problems); !slices.Contains(got, tc.field) {
				t.Errorf("Problems = %v, want one for %s", cfg.Assistants[0].Problems, tc.field)
			}
		})
	}
}

func TestLoadFatalErrors(t *testing.T) {
	cases := []struct {
		name, toml string
	}{
		{"unparseable TOML", "tick = [oops\n" + pathsTOML},
		{"relative paths.tmux", strings.Replace(pathsTOML, `"/example/store/bin/tmux"`, `"bin/tmux"`, 1)},
		{"relative paths.claude_json", strings.Replace(pathsTOML, `"/example/home/.claude.json"`, `".claude.json"`, 1)},
		{"missing paths.shell", strings.Replace(pathsTOML, "shell = \"/bin/zsh\"\n", "", 1)},
		{"non-string paths.claude", strings.Replace(pathsTOML, `"/example/store/bin/claude"`, `5`, 1)},
		{"no [paths] table", assistant("example-a")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.toml+assistant("example-b"))); err == nil {
				t.Fatal("Load returned no error")
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
			t.Fatal("Load returned no error")
		}
	})
}

// The [paths] entries that reach tmux (the shell and claude as argv tokens,
// the state dir inside an -e value) MUST NOT end in `\;`, which tmux cannot
// pass on intact; like any bad [paths] entry, that is fatal.
func TestTmuxPathsEndingInEscapedSemicolonAreFatal(t *testing.T) {
	for key, old := range map[string]string{
		"shell":     `"/bin/zsh"`,
		"claude":    `"/example/store/bin/claude"`,
		"state_dir": `"/example/state/pg-assistants"`,
	} {
		t.Run(key, func(t *testing.T) {
			body := strings.Replace(pathsTOML, old, strings.TrimSuffix(old, `"`)+`\\;"`, 1)
			_, err := Load(writeConfig(t, body+assistant("example-a")))
			var fe FieldError
			if !errors.As(err, &fe) || fe.Field != "paths."+key {
				t.Errorf("Load error = %v, want a FieldError for paths.%s", err, key)
			}
		})
	}
}

// What tmux can pass on intact is accepted: a trailing ";" (escaped at
// launch), a ";" in the middle, a prompt of just ";", a "#" outside cwd.
func TestTmuxSafeValuesAccepted(t *testing.T) {
	cfg := load(t, pathsTOML+assistant(
		"example-a",
		`name = "Example A;"`,
		`cwd = "/example/work/a;b;"`,
		`prompt = ";"`,
		`env = { EXAMPLE_FOO = "bar;", EXAMPLE_HASH = "#{x}" }`,
	)+assistant("example-b", `prompt = "Resume now; then #1;"`))
	for _, a := range cfg.Assistants {
		if len(a.Problems) != 0 {
			t.Errorf("%s Problems = %v, want none", a.ID, a.Problems)
		}
	}
	if len(cfg.Problems) != 0 {
		t.Errorf("global Problems = %v, want none", cfg.Problems)
	}
}

func TestStrictJoinsAll(t *testing.T) {
	cfg := load(t, "[grace]\nkill = \"0s\"\n"+pathsTOML+assistant("example-a", `effort = "turbo"`)+assistant("example-b"))
	err := cfg.Strict()
	if err == nil {
		t.Fatal("Strict() = nil, want an error")
	}
	for _, f := range []string{"grace.kill: ", "assistant[example-a].effort: "} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("Strict() = %q, want it to name %s", err, f)
		}
	}
	if got := len(strings.Split(err.Error(), "\n")); got != 2 {
		t.Errorf("Strict() has %d lines, want one per problem (2): %q", got, err)
	}

	if err := load(t, pathsTOML+assistant("example-a")).Strict(); err != nil {
		t.Errorf("Strict() on a valid config = %v, want nil", err)
	}
}

func TestFieldErrorString(t *testing.T) {
	e := FieldError{Field: "assistant[example-a].context.kill", Msg: "too high"}
	if got, want := e.Error(), "assistant[example-a].context.kill: too high"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestValidateAcceptsKillAboveOne(t *testing.T) {
	cfg := load(t, pathsTOML+assistant("example-a", `context.limit = 200000`, `context.kill = 1.05`))
	if a := cfg.Assistants[0]; a.Context.Window != 1_000_000 || len(a.Problems) != 0 {
		t.Errorf("window %d, Problems %v; want 1000000 and none", a.Context.Window, a.Problems)
	}
}

func TestMissingCwdIsNotAProblem(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := load(t, pathsTOML+assistant("example-a", `cwd = "`+missing+`"`))
	if got := cfg.Assistants[0].Problems; len(got) != 0 {
		t.Errorf("Problems = %v, want none for a missing but absolute cwd", got)
	}
}

func TestContextWindowExactIDsOnly(t *testing.T) {
	cases := []struct {
		model string
		want  int64
		known bool
	}{
		{"claude-sonnet-5-5", 1_000_000, true},
		{"claude-opus-5-5", 1_000_000, true},
		{"claude-haiku-5-5", 1_000_000, true},
		{"claude-fable-5-1", 1_000_000, true},
		{"claude-sonnet-4-5", 200_000, true},
		{"claude-haiku-4-5", 200_000, true},
		{"claude-sonnet-4-5[1m]", 1_000_000, true},
		{"claude-sonnet-5-5[1m]", 1_000_000, true},
		// No family fallback: an id the table does not hold is unknown.
		{"claude-sonnet-9", 0, false},
		{"claude-opus-9-9", 0, false},
		{"claude-sonnet-9[1m]", 0, false},
		{"claude-sonnet-4-5[2m]", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, known := ContextWindow(tc.model)
		if got != tc.want || known != tc.known {
			t.Errorf("ContextWindow(%q) = %d, %v; want %d, %v", tc.model, got, known, tc.want, tc.known)
		}
	}
}
