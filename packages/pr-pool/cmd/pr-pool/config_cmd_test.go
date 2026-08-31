package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestRenderConfigShow_includesDispatchScalars(t *testing.T) {
	cfg := config.Default()
	cfg.ConfigPath = "/repo/.pr-pool/config.toml"
	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()

	// existing surface preserved
	for _, want := range []string{"config path: /repo/.pr-pool/config.toml", "roles ("} {
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

// config --show prints both gate paths, and each one's "paused since" mtime
// when set, or an explicit not-paused state when absent.
func TestRenderConfigShow_gatesPathsStateMtime(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.QuotaPaused = filepath.Join(dir, "quota-paused")
	cfg.CICDDown = filepath.Join(dir, "cicd-down")
	if err := os.WriteFile(cfg.QuotaPaused, []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	renderConfigShow(&b, cfg)
	out := b.String()

	for _, want := range []string{cfg.QuotaPaused, cfg.CICDDown, "paused since", "not paused"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The mtime string itself must be present (RFC3339), not just the label.
	fi, err := os.Stat(cfg.QuotaPaused)
	if err != nil {
		t.Fatal(err)
	}
	if want := fi.ModTime().Format(time.RFC3339); !strings.Contains(out, want) {
		t.Errorf("missing mtime %q in:\n%s", want, out)
	}
}

// TestRenderConfigShowJSON_includesDispatchScalars is renderConfigShowJSON's
// counterpart to TestRenderConfigShow_includesDispatchScalars: the same
// dispatch-scalar audit payoff (pg2-ju3r), as one JSON object (Task 1.5b).
func TestRenderConfigShowJSON_includesDispatchScalars(t *testing.T) {
	cfg := config.Default()
	cfg.ConfigPath = "/repo/.pr-pool/config.toml"
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

// TestRenderConfigShowJSON_gatesPathsStateMtime is renderConfigShowJSON's
// counterpart to TestRenderConfigShow_gatesPathsStateMtime: both gate paths,
// and each one's paused state + "since" mtime (or its absence when unset), as
// typed JSON fields instead of a rendered string.
func TestRenderConfigShowJSON_gatesPathsStateMtime(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.QuotaPaused = filepath.Join(dir, "quota-paused")
	cfg.CICDDown = filepath.Join(dir, "cicd-down")
	if err := os.WriteFile(cfg.QuotaPaused, []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	renderConfigShowJSON(&b, cfg)

	var got configShowReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.Gates.QuotaPaused.Path != cfg.QuotaPaused || !got.Gates.QuotaPaused.Paused {
		t.Errorf("quotaPaused gate = %+v, want path=%q paused=true", got.Gates.QuotaPaused, cfg.QuotaPaused)
	}
	fi, err := os.Stat(cfg.QuotaPaused)
	if err != nil {
		t.Fatal(err)
	}
	if want := fi.ModTime().Format(time.RFC3339); got.Gates.QuotaPaused.Since != want {
		t.Errorf("quotaPaused since = %q, want %q", got.Gates.QuotaPaused.Since, want)
	}
	if got.Gates.CICDDown.Path != cfg.CICDDown || got.Gates.CICDDown.Paused || got.Gates.CICDDown.Since != "" {
		t.Errorf("cicdDown gate = %+v, want path=%q paused=false since=\"\"", got.Gates.CICDDown, cfg.CICDDown)
	}
}

// TestRoute_configShowJSON covers args.go's parseConfigArgs --json handling:
// --json is accepted wherever it appears alongside --show, but is a usage
// error with --print-defaults (no JSON encoding is defined for that mode).
func TestRoute_configShowJSON(t *testing.T) {
	if r := route([]string{"pr-pool", "config", "--show", "--json"}); r.kind != routeConfig || r.configMode != "show" || !r.json {
		t.Errorf("route(config --show --json) = %+v, want routeConfig show json=true", r)
	}
	if r := route([]string{"pr-pool", "config", "--json", "--show"}); r.kind != routeConfig || r.configMode != "show" || !r.json {
		t.Errorf("route(config --json --show) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pr-pool", "config", "--show"}); r.json {
		t.Errorf("route(config --show) = %+v, want json=false when --json is absent", r)
	}
	if r := route([]string{"pr-pool", "config", "--print-defaults", "--json"}); r.kind != routeUsageErr {
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
