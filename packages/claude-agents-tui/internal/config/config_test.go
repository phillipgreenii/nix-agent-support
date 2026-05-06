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
	if cfg.IdleThreshold != 10*time.Minute {
		t.Errorf("IdleThreshold default: got %v, want 10m", cfg.IdleThreshold)
	}
	if cfg.RefreshInterval != 1*time.Second {
		t.Errorf("RefreshInterval default: got %v, want 1s", cfg.RefreshInterval)
	}
	if cfg.HeadlessInterval != 5*time.Second {
		t.Errorf("HeadlessInterval default: got %v, want 5s", cfg.HeadlessInterval)
	}
	if cfg.CaffeinateGrace != 60*time.Second {
		t.Errorf("CaffeinateGrace default: got %v, want 60s", cfg.CaffeinateGrace)
	}
	if cfg.ConsecutiveIdleChecks != 3 {
		t.Errorf("ConsecutiveIdleChecks default: got %d, want 3", cfg.ConsecutiveIdleChecks)
	}
	if cfg.MaximumWait != 2*time.Hour {
		t.Errorf("MaximumWait default: got %v, want 2h", cfg.MaximumWait)
	}
}

func TestOverridesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
plan_tier = "pro"
topup_pool_usd = 50.0
topup_purchase_date = "2026-04-01"
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
	if cfg.IdleThreshold != 10*time.Minute {
		t.Errorf("IdleThreshold should retain default, got %v", cfg.IdleThreshold)
	}
	if cfg.RefreshInterval != 1*time.Second {
		t.Errorf("RefreshInterval should retain default, got %v", cfg.RefreshInterval)
	}
	if cfg.MaximumWait != 2*time.Hour {
		t.Errorf("MaximumWait should retain default, got %v", cfg.MaximumWait)
	}
}
