package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argv + "'\ncat <<'EOF'\n" + stdout + "\nEOF\nexit " +
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
	if strings.Contains(strings.ToLower(out), "narrative") {
		t.Errorf("no narrative option is part of this phase:\n%s", out)
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
