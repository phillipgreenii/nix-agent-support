package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/query"
)

func TestRenderConfigShow_includesDispatchScalars(t *testing.T) {
	cfg := config.Default()
	cfg.ConfigPath = "/repo/.pg-router/config.toml"
	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()

	// existing surface preserved
	for _, want := range []string{"config path: /repo/.pg-router/config.toml", "roles ("} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// dispatch scalars (the audit payoff) — no worker launched
	for _, want := range []string{
		"permission-mode", "dontAsk",
		"allowed-tools", cfg.AllowedTools, // verbatim
		"autonomous", "true",
		"confirm-ingest",
		"budget",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing dispatch scalar %q in:\n%s", want, out)
		}
	}
	// audit: 'git push' must be absent from the printed allowlist
	if strings.Contains(out, "git push") {
		t.Errorf("allowlist must not contain 'git push'; got:\n%s", out)
	}
}

// TestRenderConfigShowJSON_includesDispatchScalars is renderConfigShowJSON's
// counterpart to TestRenderConfigShow_includesDispatchScalars: the same
// dispatch-scalar audit payoff (pg2-ju3r), as one JSON object (Task 1.5b).
func TestRenderConfigShowJSON_includesDispatchScalars(t *testing.T) {
	cfg := config.Default()
	cfg.ConfigPath = "/repo/.pg-router/config.toml"
	var b bytes.Buffer
	renderConfigShowJSON(&b, cfg)

	var got configShowReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.ConfigPath != cfg.ConfigPath {
		t.Errorf("configPath = %q, want %q", got.ConfigPath, cfg.ConfigPath)
	}
	if len(got.Roles) != len(cfg.Roles) {
		t.Errorf("roles = %d entries, want %d", len(got.Roles), len(cfg.Roles))
	}
	if got.Dispatch.PermissionMode != "dontAsk" {
		t.Errorf("permissionMode = %q, want dontAsk", got.Dispatch.PermissionMode)
	}
	// allowed-tools verbatim (the audit payoff): the wire must never summarize it.
	if got.Dispatch.AllowedTools != cfg.AllowedTools {
		t.Errorf("allowedTools = %q, want verbatim %q", got.Dispatch.AllowedTools, cfg.AllowedTools)
	}
	if !got.Dispatch.Autonomous {
		t.Errorf("autonomous = false, want true")
	}
	// audit: 'git push' must be absent from the echoed allowlist.
	if strings.Contains(got.Dispatch.AllowedTools, "git push") {
		t.Errorf("allowlist must not contain 'git push'; got %q", got.Dispatch.AllowedTools)
	}
	// Per Task 0.4's wire decision, subcommand --json is UNVERSIONED unless
	// docs/decisions/wire.md says otherwise: this report must carry no
	// schemaVersion field.
	if bytes.Contains(b.Bytes(), []byte("schemaVersion")) {
		t.Errorf("config --show --json must not carry a schemaVersion field (unversioned, Task 0.4); got:\n%s", b.String())
	}
}

// TestRoute_configShowJSON covers args.go's parseConfigArgs --json handling:
// --json is accepted wherever it appears alongside --show, but is a usage
// error with --print-defaults (no JSON encoding is defined for that mode).
func TestRoute_configShowJSON(t *testing.T) {
	if r := route([]string{"pg-router", "config", "--show", "--json"}); r.kind != routeConfig || r.configMode != "show" || !r.json {
		t.Errorf("route(config --show --json) = %+v, want routeConfig show json=true", r)
	}
	if r := route([]string{"pg-router", "config", "--json", "--show"}); r.kind != routeConfig || r.configMode != "show" || !r.json {
		t.Errorf("route(config --json --show) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pg-router", "config", "--show"}); r.json {
		t.Errorf("route(config --show) = %+v, want json=false when --json is absent", r)
	}
	if r := route([]string{"pg-router", "config", "--print-defaults", "--json"}); r.kind != routeUsageErr {
		t.Errorf("route(config --print-defaults --json) = %+v, want a usage error", r)
	}
	for _, want := range []string{"config", "--json"} {
		if !strings.Contains(usageLine, want) {
			t.Errorf("usageLine does not mention %q", want)
		}
	}
	if !strings.Contains(helpText, "config --show [--json]") {
		t.Error("helpText does not advertise config --show [--json]")
	}
}

// config --show no longer reports gate files (bead pg2-h63eu): gates are live
// state in the running core, not configuration. It says where to look instead,
// and the --json report carries no gates object.
func TestRenderConfigShow_pointsAtTheGateRegistryInsteadOfGateFiles(t *testing.T) {
	cfg := config.Default()
	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()
	if !strings.Contains(out, "pg-router gate list") {
		t.Errorf("config --show should point at 'pg-router gate list'; got:\n%s", out)
	}
	for _, gone := range []string{"operator-paused", "cicd-down", "disk-space-low"} {
		if strings.Contains(out, gone) {
			t.Errorf("config --show still mentions the retired %q gate file; got:\n%s", gone, out)
		}
	}
	var j bytes.Buffer
	renderConfigShowJSON(&j, cfg)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(j.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["gates"]; has {
		t.Errorf("config --show --json still carries a gates object: %s", j.String())
	}
}

// config --show and --show --json print each [[query]]'s command and strict
// switch, and nothing of the kind for a role.
func TestRenderConfigShow_printsEachQuerysCommandAndStrictSwitch(t *testing.T) {
	cfg := config.Default()
	cfg.Queries = query.SourceSet{
		{Name: "strict", Query: query.CommandQuery{
			Meta:          query.Meta{EmitTypes: []string{"e"}},
			Argv:          []string{"/opt/producer", "--flag", "two words"},
			Format:        query.FormatJSONL,
			StrictBaseEnv: true,
		}},
		{Name: "plain", Query: query.CommandQuery{Meta: query.Meta{EmitTypes: []string{"e"}}, Argv: []string{"/opt/plain"}, Format: query.FormatJSONL}},
		{Name: "tick", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t"}}}},
	}
	wantLine := "/opt/producer --flag 'two words'"

	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()
	if !strings.Contains(out, "command: "+wantLine) {
		t.Errorf("text form lacks the command %q:\n%s", wantLine, out)
	}
	if !strings.Contains(out, "strict_base_env: true") || !strings.Contains(out, "strict_base_env: false") {
		t.Errorf("text form lacks the strict_base_env switch for both command queries:\n%s", out)
	}
	if !strings.Contains(out, "command: /opt/plain") {
		t.Errorf("a plain command query still prints its command:\n%s", out)
	}
	if n := strings.Count(out, "command:"); n != 2 {
		t.Errorf("want a command line for the two command queries only (timer has none), got %d:\n%s", n, out)
	}

	var j bytes.Buffer
	renderConfigShowJSON(&j, cfg)
	var got configShowReport
	if err := json.Unmarshal(j.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Queries) != 3 {
		t.Fatalf("queries = %d, want 3", len(got.Queries))
	}
	if got.Queries[0].Command != wantLine || got.Queries[0].StrictBaseEnv == nil || !*got.Queries[0].StrictBaseEnv {
		t.Errorf("strict query json = %+v, want command %q and strictBaseEnv true", got.Queries[0], wantLine)
	}
	if got.Queries[1].Command != "/opt/plain" || got.Queries[1].StrictBaseEnv == nil || *got.Queries[1].StrictBaseEnv {
		t.Errorf("plain query json = %+v, want strictBaseEnv false", got.Queries[1])
	}
	if got.Queries[2].Command != "" || got.Queries[2].StrictBaseEnv != nil {
		t.Errorf("timer query json carries a command: %+v", got.Queries[2])
	}
	var raw map[string]any
	if err := json.Unmarshal(j.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw["roles"].([]any) {
		if _, has := r.(map[string]any)["command"]; has {
			t.Errorf("a role carries a composed command: %v", r)
		}
	}
}
