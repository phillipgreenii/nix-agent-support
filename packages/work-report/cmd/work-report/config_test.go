package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

// runCLI runs the root command with args and returns stdout and the error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// isolate points the config and state homes at empty temp dirs so a developer's
// real config never leaks into a test.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	// a developer's tracker pin must not leak into the discovery tests
	t.Setenv("WORK_REPORT_ISSUE_BACKEND", "")
	return home
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeConnector puts an executable pg-connector first on PATH that prints
// stdout, records its argv to <dir>/argv, and exits with code. It returns the
// argv file path.
func fakeConnector(t *testing.T, stdout string, code int) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	// Shell builtins only (printf, exit): PATH below is just this dir plus
	// /usr/bin:/bin, and the nix build sandbox has no coreutils there, so an
	// external `cat` is "command not found".
	quoted := "'" + strings.ReplaceAll(stdout, "'", `'\''`) + "'"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argv + "'\nprintf '%s\\n' " + quoted + "\nexit " +
		string(rune('0'+code)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-connector"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return argv
}

func TestConfigPGRouterQueryDefaults(t *testing.T) {
	isolate(t)
	out, err := runCLI(t, "config", "pg-router-query")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`emits = ["escalated.pg2"]`,
		`every = "1h"`,
		`"last-48h"`,
		`name = "work-report-pull"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stanza lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "work-report.degraded") {
		t.Errorf("stanza must not emit work-report.degraded:\n%s", out)
	}
}

func TestConfigPGRouterQueryCustomSchedule(t *testing.T) {
	isolate(t)
	cfg := writeCfg(t, "schedule:\n  interval: 30m\n  window: 7d\n")
	out, err := runCLI(t, "--config", cfg, "config", "pg-router-query")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`every = "30m"`, `"last-7d"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stanza lacks %s:\n%s", want, out)
		}
	}
}

func TestConfigPGRouterQueryJSONShape(t *testing.T) {
	isolate(t)
	out, err := runCLI(t, "--output", "json", "config", "pg-router-query")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Errorf("keys = %v, want only text", got)
	}
	text, _ := got["text"].(string)
	if !strings.Contains(text, `emits = ["escalated.pg2"]`) {
		t.Errorf("text = %q", text)
	}
}

func TestConfigShowJSONShape(t *testing.T) {
	isolate(t)
	cfg := writeCfg(t, `
timezone: America/Chicago
sources:
  backend-a:
    enable: false
    labels: [l1]
  backend-b:
    labels: [l2]
store:
  path: /custom/s.db
generators:
  narrative:
    command: [/bin/wr-narrative]
    timeout: 10m
    inputs: [baseline]
    env: [HTTPS_PROXY]
reports:
  daily:
    generator: narrative
    range: yesterday
    narrowing: { labels: [l1] }
    config:
      model: some-model
      apiKey: hunter2
      monkey: { a: 1 }
      nested: { sortKey: x, keep: y }
    output:
      dir: /out/daily
`)
	out, err := runCLI(t, "--config", cfg, "--output", "json", "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Timezone string `json:"timezone"`
		Sources  map[string]struct {
			Enable bool     `json:"enable"`
			Labels []string `json:"labels"`
		} `json:"sources"`
		Schedule struct {
			Interval string `json:"interval"`
			Window   string `json:"window"`
		} `json:"schedule"`
		Store struct {
			Path string `json:"path"`
		} `json:"store"`
		Generators map[string]struct {
			Builtin bool     `json:"builtin"`
			Command []string `json:"command"`
			Timeout string   `json:"timeout"`
			Inputs  []string `json:"inputs"`
			Env     []string `json:"env"`
		} `json:"generators"`
		Presets map[string]struct {
			Builtin   bool           `json:"builtin"`
			Generator string         `json:"generator"`
			Config    map[string]any `json:"config"`
			Range     string         `json:"range"`
			Narrowing struct {
				Labels, Sources, Types []string
			} `json:"narrowing"`
			Output struct {
				Dir string `json:"dir"`
			} `json:"output"`
		} `json:"presets"`
		Problems []map[string]any `json:"problems"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("shape: %v\n%s", err, out)
	}
	if got.Timezone != "America/Chicago" {
		t.Errorf("timezone = %q", got.Timezone)
	}
	if a := got.Sources["backend-a"]; a.Enable || len(a.Labels) != 1 || a.Labels[0] != "l1" {
		t.Errorf("backend-a = %+v", a)
	}
	if b := got.Sources["backend-b"]; !b.Enable || len(b.Labels) != 1 || b.Labels[0] != "l2" {
		t.Errorf("backend-b = %+v", b)
	}
	if got.Schedule.Interval != "1h" || got.Schedule.Window != "48h" {
		t.Errorf("schedule = %+v", got.Schedule)
	}
	if got.Store.Path != "/custom/s.db" {
		t.Errorf("store.path = %q", got.Store.Path)
	}
	if len(got.Problems) != 0 {
		t.Errorf("problems = %v", got.Problems)
	}
	n := got.Generators["narrative"]
	if n.Builtin || len(n.Command) != 1 || n.Command[0] != "/bin/wr-narrative" || n.Timeout != "10m" ||
		len(n.Inputs) != 1 || n.Inputs[0] != "baseline" || len(n.Env) != 1 || n.Env[0] != "HTTPS_PROXY" {
		t.Errorf("generators.narrative = %+v", n)
	}
	if b := got.Generators["baseline"]; !b.Builtin {
		t.Errorf("generators.baseline = %+v, want built in", b)
	}
	d := got.Presets["daily"]
	if d.Generator != "narrative" || d.Range != "yesterday" || d.Output.Dir != "/out/daily" ||
		len(d.Narrowing.Labels) != 1 || d.Narrowing.Sources == nil || d.Narrowing.Types == nil {
		t.Errorf("presets.daily = %+v", d)
	}
	wantCfg := map[string]any{"model": "some-model", "apiKey": "***", "monkey": "***", "nested": map[string]any{"sortKey": "***", "keep": "y"}}
	if !reflect.DeepEqual(d.Config, wantCfg) {
		t.Errorf("presets.daily.config = %#v, want %#v", d.Config, wantCfg)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("config show leaked a redacted value:\n%s", out)
	}
	if b := got.Presets["baseline"]; !b.Builtin || b.Generator != "baseline" || b.Range != "today" {
		t.Errorf("presets.baseline = %+v", b)
	}
}

func TestConfigShowDefaultsHaveBuiltinBaselineAndNoProblems(t *testing.T) {
	home := isolate(t)
	out, err := runCLI(t, "--config", writeCfg(t, ""), "--output", "json", "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["kinds"]; ok {
		t.Error("config show must not print a kinds key")
	}
	if probs, ok := got["problems"].([]any); !ok || len(probs) != 0 {
		t.Errorf("problems = %v, want []", got["problems"])
	}
	base := got["presets"].(map[string]any)["baseline"].(map[string]any)
	if dir := base["output"].(map[string]any)["dir"]; dir != filepath.Join(home, "state", "work-report", "reports", "baseline") {
		t.Errorf("baseline output.dir = %v", dir)
	}
	if cfg, ok := base["config"].(map[string]any); !ok || len(cfg) != 0 {
		t.Errorf("baseline config = %v, want {}", base["config"])
	}
}

func TestConfigShowBrokenSectionsPrintProblemsAndExitZero(t *testing.T) {
	isolate(t)
	cfg := writeCfg(t, `timezone: UTC
generators:
  Bad Name: {command: [x]}
  ok: {command: [x]}
reports:
  fine: {generator: ok}
  dangling: {generator: nope}
`)
	out, err := runCLI(t, "--config", cfg, "--output", "json", "config", "show")
	if err != nil {
		t.Fatalf("config show must exit 0 with problems: %v", err)
	}
	var got struct {
		Presets  map[string]any `json:"presets"`
		Problems []struct {
			Kind      string  `json:"kind"`
			Generator *string `json:"generator"`
			Preset    *string `json:"preset"`
		} `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Presets["fine"]; !ok {
		t.Errorf("valid preset missing: %v", got.Presets)
	}
	if len(got.Problems) != 2 {
		t.Fatalf("problems = %+v, want 2", got.Problems)
	}
	if p := got.Problems[0]; p.Kind != "bad_name" || p.Generator == nil || *p.Generator != "Bad Name" || p.Preset != nil {
		t.Errorf("problems[0] = %+v", p)
	}
	if p := got.Problems[1]; p.Kind != "unknown_generator" || p.Preset == nil || *p.Preset != "dangling" || p.Generator != nil {
		t.Errorf("problems[1] = %+v", p)
	}

	out, err = runCLI(t, "--config", cfg, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"problems:", "bad_name:", "unknown_generator:", "fine: generator=ok range=today"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}
}

// A config carrying the retired kinds key fails Load, so every verb that
// loads the config exits 1 (an error that is not an exitError) with the
// migration text.
func TestRetiredKindsKeyFailsEveryVerb(t *testing.T) {
	for _, body := range []string{"kinds: {}\n", "kinds:\n  narrative:\n    model: m\n    systemPromptFile: /p.md\n"} {
		isolate(t)
		cfg := writeCfg(t, body)
		storePath := filepath.Join(t.TempDir(), "s.db")
		for _, args := range [][]string{
			{"pull", "--range", "today"},
			{"query"},
			{"status"},
			{"report"},
			{"config", "show"},
			{"config", "pg-router-query"},
		} {
			full := append([]string{"--config", cfg, "--store", storePath}, args...)
			_, err := runCLI(t, full...)
			if err == nil {
				t.Errorf("%v with kinds %q: want an error", args, body)
				continue
			}
			var ee *exitError
			if errors.As(err, &ee) {
				t.Errorf("%v: err is an exitError (code %d); want a plain error so main exits 1", args, ee.code)
			}
			if !errors.Is(err, config.ErrKindsRetired) {
				t.Errorf("%v: err = %v, want ErrKindsRetired", args, err)
			}
			for _, want := range []string{"reports.<name>.generator: narrative", "reports.<name>.config.promptFile"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%v: error %q lacks migration text %q", args, err, want)
				}
			}
		}
	}
}

// A broken generators or reports section must not affect pull, query, status
// or the baseline report.
func TestBrokenGeneratorsAndReportsDoNotAffectOtherVerbs(t *testing.T) {
	isolate(t)
	cfg := writeCfg(t, "timezone: UTC\ngenerators: [not, a, mapping]\nreports:\n  p: {generator: nope, range: bogus}\n")
	storePath := seedQueryStore(t)
	for _, args := range [][]string{{"query"}, {"status"}, {"report", "--range", "2026-03-01"}, {"config", "pg-router-query"}} {
		full := append([]string{"--config", cfg, "--store", storePath}, args...)
		if _, err := runCLI(t, full...); err != nil {
			t.Errorf("%v with broken generators/reports: %v", args, err)
		}
	}
}

func TestConfigShowStoreFlagWinsAndDefaultsResolve(t *testing.T) {
	home := isolate(t)
	out, err := runCLI(t, "--output", "json", "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Store struct{ Path string } `json:"store"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "state", "work-report", "store.db"); got.Store.Path != want {
		t.Errorf("default store path = %q, want %q", got.Store.Path, want)
	}

	out, err = runCLI(t, "--store", "/flag/s.db", "--output", "json", "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Store.Path != "/flag/s.db" {
		t.Errorf("--store must win, got %q", got.Store.Path)
	}
}

func TestConfigShowHuman(t *testing.T) {
	isolate(t)
	cfg := writeCfg(t, "sources:\n  backend-a:\n    enable: false\n    labels: [l1, l2]\n")
	out, err := runCLI(t, "--config", cfg, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"timezone: (system)", "schedule.interval: 1h", "schedule.window: 48h", "backend-a: enable=false labels=l1, l2"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}
	for _, want := range []string{"generators:", "baseline: built in", "presets:", "baseline: generator=baseline range=today"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kinds") {
		t.Errorf("human output mentions kinds:\n%s", out)
	}
}

func TestConfigRejectsBadOutputFormat(t *testing.T) {
	isolate(t)
	for _, verb := range []string{"show", "validate", "pg-router-query"} {
		if _, err := runCLI(t, "--output", "pg-router", "config", verb); err == nil {
			t.Errorf("config %s: --output pg-router must be rejected", verb)
		}
	}
}

func TestConfigVerbsReportBadConfig(t *testing.T) {
	isolate(t)
	bad := writeCfg(t, "bogus: true\n")
	for _, verb := range []string{"show", "pg-router-query", "validate"} {
		_, err := runCLI(t, "--config", bad, "config", verb)
		if err == nil || !strings.Contains(err.Error(), "bogus") {
			t.Errorf("config %s: err = %v, want one naming the unknown key", verb, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := runCLI(t, "--config", missing, "config", "show"); err == nil {
		t.Error("a missing explicit --config must be an error")
	}
}

func TestConfigValidateRunsPGConnector(t *testing.T) {
	isolate(t)
	argv := fakeConnector(t, "all sources ok", 0)
	out, err := runCLI(t, "config", "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "all sources ok") {
		t.Errorf("validate output lacks pg-connector's output:\n%s", out)
	}
	got, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("pg-connector was not run: %v", err)
	}
	if strings.TrimSpace(string(got)) != "config validate" {
		t.Errorf("pg-connector argv = %q, want \"config validate\"", got)
	}
}

func TestConfigValidateJSONShape(t *testing.T) {
	isolate(t)
	fakeConnector(t, "fine", 0)
	out, err := runCLI(t, "--output", "json", "config", "validate")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		OK          bool   `json:"ok"`
		ConnectorAt string `json:"pg_connector_path"`
		ExitCode    int    `json:"pg_connector_exit_code"`
		Output      string `json:"pg_connector_output"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("shape: %v\n%s", err, out)
	}
	if !got.OK || got.ExitCode != 0 || !strings.Contains(got.Output, "fine") || !strings.HasSuffix(got.ConnectorAt, "pg-connector") {
		t.Errorf("got %+v", got)
	}
}

func TestConfigValidatePGConnectorMissing(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", t.TempDir())

	_, err := runCLI(t, "config", "validate")
	if err == nil {
		t.Fatal("validate must fail when pg-connector is not on PATH")
	}
	if !strings.Contains(err.Error(), "pg-connector") {
		t.Errorf("error %q must name the missing binary", err)
	}

	out, err := runCLI(t, "--output", "json", "config", "validate")
	if err == nil {
		t.Fatal("json validate must fail too")
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("not JSON: %v\n%s", jerr, out)
	}
	if got.OK || !strings.Contains(got.Error, "pg-connector") {
		t.Errorf("got %+v", got)
	}
}

func TestConfigValidatePGConnectorFails(t *testing.T) {
	isolate(t)
	fakeConnector(t, "backend x: not ok", 3)

	out, err := runCLI(t, "config", "validate")
	if err == nil {
		t.Fatal("validate must fail when pg-connector config validate exits non-zero")
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("error %q must carry the exit code", err)
	}
	if !strings.Contains(out, "backend x: not ok") {
		t.Errorf("pg-connector's output must be shown:\n%s", out)
	}
}

func TestConfigValidateChecksConfigFirst(t *testing.T) {
	isolate(t)
	argv := fakeConnector(t, "", 0)
	bad := writeCfg(t, "schedule:\n  window: 90m\n")
	_, err := runCLI(t, "--config", bad, "config", "validate")
	if err == nil || !strings.Contains(err.Error(), "schedule.window") {
		t.Errorf("err = %v, want the invalid window", err)
	}
	if _, statErr := os.Stat(argv); statErr == nil {
		t.Error("pg-connector must not run when the config itself is invalid")
	}
}
