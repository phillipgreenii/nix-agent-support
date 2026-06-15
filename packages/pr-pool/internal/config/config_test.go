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
	if d.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions (preserves worker bypass behavior)", d.PermissionMode)
	}
	if d.SessionPrefix != "pr-pool-" {
		t.Errorf("SessionPrefix = %q, want pr-pool-", d.SessionPrefix)
	}
	if !d.FeedbackEnabled || !d.WorkerEnabled {
		t.Error("roles should default enabled")
	}
}

func TestLoad_envOverrides(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "3")
	t.Setenv("PR_POOL_MAX_WAIT", "60")
	t.Setenv("PR_POOL_BEADS_PREFIX", "pg2")
	t.Setenv("PR_POOL_MODEL", "claude-opus-4-8")
	t.Setenv("PR_POOL_PERMISSION_MODE", "plan")
	t.Setenv("PR_POOL_WORKER_ENABLED", "0")
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
	if c.PermissionMode != "plan" {
		t.Errorf("PR_POOL_PERMISSION_MODE = %q, want plan", c.PermissionMode)
	}
	if c.WorkerEnabled {
		t.Error("PR_POOL_WORKER_ENABLED=0 should disable worker role")
	}
	if !c.FeedbackEnabled {
		t.Error("feedback role should stay enabled (default) when only worker is disabled")
	}
}

func TestLoad_badIntFallsBackToDefault(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "notanint")
	if c := Load(); c.MaxWorker != 1 {
		t.Errorf("bad int should fall back to default 1, got %d", c.MaxWorker)
	}
}

func TestWorkerBudget_defaults(t *testing.T) {
	b := Default().WorkerBudget()
	if !b.Tokens.Unlimited() || !b.Cost.Unlimited() {
		t.Error("token/cost default must be unlimited")
	}
	if b.Time != 25*time.Minute {
		t.Errorf("time default = %v, want 25m (< MaxWait 30m)", b.Time)
	}
	if b.Thresholds.Reminder != 0.725 || b.Thresholds.Cancel != 0.90 || b.Thresholds.Hard != 1.0 {
		t.Errorf("thresholds = %+v", b.Thresholds)
	}
	if b.Time >= Default().MaxWait {
		t.Errorf("budget time %v must be < MaxWait %v", b.Time, Default().MaxWait)
	}
}

func TestWorkerBudget_envOverrides(t *testing.T) {
	t.Setenv("PR_POOL_BUDGET_TOKENS", "1000000")
	t.Setenv("PR_POOL_BUDGET_TIME", "600")
	b := Load().WorkerBudget()
	if int64(b.Tokens) != 1000000 || b.Time != 600*time.Second {
		t.Errorf("env overrides not applied: %+v", b)
	}
}
