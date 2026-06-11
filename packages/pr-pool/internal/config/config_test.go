package config

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	d := Default()
	if d.BeadsPrefix != "zr" {
		t.Errorf("BeadsPrefix = %q, want zr", d.BeadsPrefix)
	}
	if d.MaxFeedback != 1 || d.MaxWorker != 1 {
		t.Errorf("caps = %d/%d, want 1/1", d.MaxFeedback, d.MaxWorker)
	}
	if d.MaxWait != 1800*time.Second {
		t.Errorf("MaxWait = %v, want 1800s", d.MaxWait)
	}
	if d.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", d.PollInterval)
	}
	if d.Effort != "max" {
		t.Errorf("Effort = %q, want max", d.Effort)
	}
	if !d.Dangerous {
		t.Error("Dangerous should default true")
	}
	if d.SessionPrefix != "pr-pool-" {
		t.Errorf("SessionPrefix = %q, want pr-pool-", d.SessionPrefix)
	}
}

func TestLoad_envOverrides(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "3")
	t.Setenv("PR_POOL_MAX_WAIT", "60")
	t.Setenv("PR_POOL_BEADS_PREFIX", "pg2")
	t.Setenv("PR_POOL_MODEL", "claude-opus-4-8")
	t.Setenv("PR_POOL_DANGEROUS", "0")
	c := Load()
	if c.MaxWorker != 3 {
		t.Errorf("MaxWorker = %d, want 3", c.MaxWorker)
	}
	if c.MaxWait != 60*time.Second {
		t.Errorf("MaxWait = %v, want 60s", c.MaxWait)
	}
	if c.BeadsPrefix != "pg2" {
		t.Errorf("BeadsPrefix = %q, want pg2", c.BeadsPrefix)
	}
	if c.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.Dangerous {
		t.Error("PR_POOL_DANGEROUS=0 should disable Dangerous")
	}
}

func TestLoad_badIntFallsBackToDefault(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "notanint")
	if c := Load(); c.MaxWorker != 1 {
		t.Errorf("bad int should fall back to default 1, got %d", c.MaxWorker)
	}
}
