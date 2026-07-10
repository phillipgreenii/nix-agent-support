package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nonexistent.toml"))
	if err != nil {
		t.Fatalf("missing file should use defaults, got error: %v", err)
	}
	if cfg.PlanTier != "max_5x" {
		t.Errorf("PlanTier default: got %q, want %q", cfg.PlanTier, "max_5x")
	}
	if cfg.WorkingThreshold != 30*time.Second {
		t.Errorf("WorkingThreshold default: got %v, want 30s", cfg.WorkingThreshold)
	}
	if cfg.IdleThreshold != 20*time.Minute {
		t.Errorf("IdleThreshold default: got %v, want 20m", cfg.IdleThreshold)
	}
	if cfg.RefreshInterval != 1*time.Second {
		t.Errorf("RefreshInterval default: got %v, want 1s", cfg.RefreshInterval)
	}
	if cfg.CaffeinateGrace != 60*time.Second {
		t.Errorf("CaffeinateGrace default: got %v, want 60s", cfg.CaffeinateGrace)
	}
}

func TestAccountPricingParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
plan_tier = "max_5x"

[account.pricing.default]
input_per_mtok = 5.0
output_per_mtok = 25.0
cache_creation_per_mtok = 6.25
cache_read_per_mtok = 0.5

[account.pricing.models."claude-sonnet-4-6"]
input_per_mtok = 3.0
output_per_mtok = 15.0
cache_creation_per_mtok = 3.75
cache_read_per_mtok = 0.3
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pricing.Default.OutputPerMTok != 25.0 {
		t.Errorf("Default.OutputPerMTok = %v, want 25", cfg.Pricing.Default.OutputPerMTok)
	}
	m, ok := cfg.Pricing.Models["claude-sonnet-4-6"]
	if !ok {
		t.Fatal("sonnet model prices missing")
	}
	if m.InputPerMTok != 3.0 || m.CacheReadPerMTok != 0.3 {
		t.Errorf("sonnet prices = %+v, want in3 cr0.3", m)
	}
}

func TestAccountPricingDefaultsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nonexistent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Built-in defaults MUST populate a usable price table so native cost works
	// with no [account.pricing] config (cost is notional but must still emit).
	if cfg.Pricing.Default.OutputPerMTok <= 0 {
		t.Errorf("Pricing.Default.OutputPerMTok = %v, want a positive built-in default", cfg.Pricing.Default.OutputPerMTok)
	}
	if len(cfg.Pricing.Models) == 0 {
		t.Error("Pricing.Models empty; want built-in model prices as defaults")
	}
}

// TestAccountPricingZeroPricedModel guards that a model configured with all-zero
// prices (a legitimately free / zero-rated model) surfaces as all-zero, not the
// Default (Opus) rates. applyPricing seeds a new model from Default only on
// genuine map-absence (two-value read), so the explicit zeros win.
func TestAccountPricingZeroPricedModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[account.pricing.models."free-model"]
input_per_mtok = 0.0
output_per_mtok = 0.0
cache_creation_per_mtok = 0.0
cache_read_per_mtok = 0.0
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Pricing.Models["free-model"]
	if m != (ModelPricing{}) {
		t.Errorf("free-model prices = %+v, want all-zero (not re-seeded from Default)", m)
	}
}

func TestOverridesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
plan_tier = "pro"
topup_pool_usd = 50.0
working_threshold_s = 15
idle_threshold_s = 300
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlanTier != "pro" {
		t.Errorf("PlanTier: got %q", cfg.PlanTier)
	}
	if cfg.TopupPoolUSD != 50.0 {
		t.Errorf("TopupPoolUSD: got %v", cfg.TopupPoolUSD)
	}
	if cfg.WorkingThreshold != 15*time.Second {
		t.Errorf("WorkingThreshold: got %v", cfg.WorkingThreshold)
	}
	if cfg.IdleThreshold != 5*time.Minute {
		t.Errorf("IdleThreshold: got %v", cfg.IdleThreshold)
	}
}

func TestConfigDefaultsAutoResume(t *testing.T) {
	cfg := defaults()
	if cfg.AutoResumeDelay != 45*time.Second {
		t.Errorf("AutoResumeDelay = %v, want 45s", cfg.AutoResumeDelay)
	}
	if cfg.AutoResumeMessage != "continue" {
		t.Errorf("AutoResumeMessage = %q, want \"continue\"", cfg.AutoResumeMessage)
	}
}

// TestStaleAfterDefault: the status-line rate_limits staleness window (ADR 0021
// §1). A value older than StaleAfter renders as stale(age).
func TestStaleAfterDefault(t *testing.T) {
	cfg := defaults()
	if cfg.StaleAfter != 10*time.Minute {
		t.Errorf("StaleAfter default = %v, want 10m", cfg.StaleAfter)
	}
}

func TestStaleAfterOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("stale_after_s = 120\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StaleAfter != 120*time.Second {
		t.Errorf("StaleAfter = %v, want 120s", cfg.StaleAfter)
	}
}

func TestConfigDefaultsCmuxSidebar(t *testing.T) {
	cfg := defaults()
	if !cfg.CmuxSidebarEnable {
		t.Errorf("CmuxSidebarEnable = %v, want true by default", cfg.CmuxSidebarEnable)
	}
	if cfg.CmuxSidebarIntervalTicks != 5 {
		t.Errorf("CmuxSidebarIntervalTicks = %v, want 5", cfg.CmuxSidebarIntervalTicks)
	}
}

func TestConfigDefaultsNudge(t *testing.T) {
	cfg := defaults()
	if cfg.DisruptGrace != 30*time.Second {
		t.Errorf("DisruptGrace default = %v, want 30s", cfg.DisruptGrace)
	}
	if cfg.EscalationAfter != 60*time.Second {
		t.Errorf("EscalationAfter default = %v, want 60s", cfg.EscalationAfter)
	}
}

func TestConfigOverridesNudge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
disrupt_grace_s = 45
escalation_after_s = 90
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisruptGrace != 45*time.Second {
		t.Errorf("DisruptGrace = %v, want 45s", cfg.DisruptGrace)
	}
	if cfg.EscalationAfter != 90*time.Second {
		t.Errorf("EscalationAfter = %v, want 90s", cfg.EscalationAfter)
	}
}

func TestPartialOverridePreservesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// Only override one field — every other field must retain its default.
	if err := os.WriteFile(path, []byte(`working_threshold_s = 15`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkingThreshold != 15*time.Second {
		t.Errorf("WorkingThreshold override: got %v, want 15s", cfg.WorkingThreshold)
	}
	if cfg.PlanTier != "max_5x" {
		t.Errorf("PlanTier should retain default, got %q", cfg.PlanTier)
	}
	if cfg.IdleThreshold != 20*time.Minute {
		t.Errorf("IdleThreshold should retain default (20m), got %v", cfg.IdleThreshold)
	}
	if cfg.RefreshInterval != 1*time.Second {
		t.Errorf("RefreshInterval should retain default, got %v", cfg.RefreshInterval)
	}
}

func TestConfigOTelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[otel]
endpoint = "http://127.0.0.1:4317"

[otel.resource_attributes]
"deployment.environment" = "local"
"host.name" = "mbp-02"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OTel.Endpoint != "http://127.0.0.1:4317" {
		t.Errorf("OTel.Endpoint = %q", cfg.OTel.Endpoint)
	}
	if cfg.OTel.ResourceAttrs["host.name"] != "mbp-02" {
		t.Errorf("OTel.ResourceAttrs = %+v", cfg.OTel.ResourceAttrs)
	}
}

func TestConfigOTelDefaultsEmpty(t *testing.T) {
	cfg := defaults()
	if cfg.OTel.Endpoint != "" || len(cfg.OTel.ResourceAttrs) != 0 {
		t.Errorf("OTel default must be empty, got %+v", cfg.OTel)
	}
}

func TestApplyOTelEnvSetsWhenUnset(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	os.Unsetenv("OTEL_RESOURCE_ATTRIBUTES")
	ApplyOTelEnv(OTelConfig{
		Endpoint:      "http://127.0.0.1:4317",
		ResourceAttrs: map[string]string{"host.name": "mbp-02"},
	})
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://127.0.0.1:4317" {
		t.Errorf("endpoint env = %q", got)
	}
	if got := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); got != "host.name=mbp-02" {
		t.Errorf("resource attrs env = %q", got)
	}
}

func TestApplyOTelEnvLeavesExplicitEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://explicit:4317")
	ApplyOTelEnv(OTelConfig{Endpoint: "http://config:4317"})
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://explicit:4317" {
		t.Errorf("explicit env must win, got %q", got)
	}
}

// TestConfigDecoratorsRoundTrip verifies that [[decorator]] blocks parse into
// cfg.Decorators with name/command/timeout_ms preserved. The decorator path is
// the only sanctioned extension point for downstream-org labels (see ADR-0011).
func TestConfigDecoratorsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[[decorator]]
name = "zr-labels"
command = "/nix/store/abc-pa-monitor-decorator-zr/bin/pa-monitor-decorator-zr"
timeout_ms = 1500

[[decorator]]
name = "gc-labels"
command = "/nix/store/def-pa-monitor-decorator-gc/bin/pa-monitor-decorator-gc"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Decorators) != 2 {
		t.Fatalf("Decorators: got %d entries, want 2", len(cfg.Decorators))
	}
	if cfg.Decorators[0].Name != "zr-labels" || cfg.Decorators[0].TimeoutMS != 1500 {
		t.Errorf("Decorators[0] = %+v", cfg.Decorators[0])
	}
	if cfg.Decorators[1].Name != "gc-labels" || cfg.Decorators[1].TimeoutMS != 0 {
		t.Errorf("Decorators[1] = %+v (TimeoutMS 0 means use runner default)", cfg.Decorators[1])
	}
}
