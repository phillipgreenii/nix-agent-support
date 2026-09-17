package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// TestLoadConfig_rejectsInvalidPermissionMode proves loadConfig — this
// module's own config-decoding entrypoint (dispatch.go calls it on every
// dispatch) — rejects an invalid claude --permission-mode value AT ITS OWN
// config-load time (docket pg2-oju6w Task 5.7's acceptance criterion), by
// way of config.Config.Validate().
func TestLoadConfig_rejectsInvalidPermissionMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"permissionMode":"yolo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err == nil {
		t.Fatal("loadConfig with an invalid permissionMode must fail, got nil error")
	} else if !strings.Contains(err.Error(), "invalid permissionMode") {
		t.Errorf("loadConfig error must name the invalid permissionMode; got %v", err)
	}
}

// TestLoadConfig_acceptsValidPermissionMode is the positive control: a real
// claude --permission-mode value decodes and validates cleanly.
func TestLoadConfig_acceptsValidPermissionMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"permissionMode":"plan"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig with a valid permissionMode must succeed: %v", err)
	}
	if cfg.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want plan", cfg.PermissionMode)
	}
}

// TestLoadConfig_emptyPathValidatesDefault proves the empty-path/Default()
// shortcut is validated too, not skipped.
func TestLoadConfig_emptyPathValidatesDefault(t *testing.T) {
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig(\"\") must succeed against config.Default(): %v", err)
	}
	if cfg.PermissionMode != "dontAsk" {
		t.Errorf("PermissionMode = %q, want dontAsk (config.Default())", cfg.PermissionMode)
	}
}

// budgetRoleTemplate is a minimal valid ccpool roleFile carrying a
// budget.tokens/budget.cost/budget.time block, used by the budget-decode
// tests below (pg2-r8al1).
const budgetRoleTemplate = `{
	"name": "worker",
	"type": "ccpool",
	"ccpool": {
		"actor": "pgii-pool__worker",
		"completion": "close-or-handback",
		"onFailure": "add-human",
		"onDispatchFail": "leave",
		"promptBody": "do the work",
		"budget": {"tokens": 5000, "cost": 250, "time": "25m"}
	}
}`

func mustWriteRoleFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "role.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadRole_decodesCCPoolBudget proves --role-config JSON's
// budget.tokens/budget.cost/budget.time decode into
// roles.CCPoolConfig.Budget correctly (pg2-r8al1 acceptance criterion 1) —
// before this, roleFile.CCPool carried no Budget field at all, so a role
// config could never populate anything but the unlimited Go zero value.
func TestLoadRole_decodesCCPoolBudget(t *testing.T) {
	role, err := loadRole(mustWriteRoleFile(t, budgetRoleTemplate))
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	got := role.CCPool.Budget
	if got.Tokens != 5000 {
		t.Errorf("Budget.Tokens = %v, want 5000", got.Tokens)
	}
	if got.Cost != 250 {
		t.Errorf("Budget.Cost = %v, want 250", got.Cost)
	}
	if got.Time != 25*time.Minute {
		t.Errorf("Budget.Time = %v, want 25m", got.Time)
	}
}

// TestLoadRole_ccpoolBudgetAbsentIsUnlimited is the positive control: a
// roleFile with no "budget" block at all still decodes cleanly to the
// unlimited zero value (today's unchanged behavior for a role that never
// wants a watchdog, e.g. "feedback").
func TestLoadRole_ccpoolBudgetAbsentIsUnlimited(t *testing.T) {
	role, err := loadRole(mustWriteRoleFile(t, `{
		"name": "feedback",
		"type": "ccpool",
		"ccpool": {
			"actor": "pgii-pool__process-feedback",
			"completion": "close-only",
			"onFailure": "unclaim",
			"onDispatchFail": "unclaim",
			"promptBody": "process feedback"
		}
	}`))
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	b := role.CCPool.Budget
	if !b.Tokens.Unlimited() || !b.Cost.Unlimited() || b.Time > 0 {
		t.Errorf("no budget block should decode to the unlimited zero value, got %+v", b)
	}
}

// TestLoadRole_ccpoolBudgetExplicitZeroTimeIsUnlimited proves an explicit
// `"time": "0s"` (the old schema's own way to deliberately request
// "unlimited, no watchdog" per role — the bug report's "feedback" example)
// decodes to Time == 0, not an error and not a nonzero duration.
func TestLoadRole_ccpoolBudgetExplicitZeroTimeIsUnlimited(t *testing.T) {
	role, err := loadRole(mustWriteRoleFile(t, `{
		"name": "feedback",
		"type": "ccpool",
		"ccpool": {
			"actor": "pgii-pool__process-feedback",
			"completion": "close-only",
			"onFailure": "unclaim",
			"onDispatchFail": "unclaim",
			"promptBody": "process feedback",
			"budget": {"time": "0s"}
		}
	}`))
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	if role.CCPool.Budget.Time != 0 {
		t.Errorf("explicit budget.time=0s must decode to Time==0 (unlimited), got %v", role.CCPool.Budget.Time)
	}
}

// TestLoadRole_ccpoolBudgetInvalidTimeErrors proves a malformed budget.time
// fails loadRole with an error naming budget.time, rather than silently
// zeroing it or panicking.
func TestLoadRole_ccpoolBudgetInvalidTimeErrors(t *testing.T) {
	p := mustWriteRoleFile(t, `{
		"name": "worker",
		"type": "ccpool",
		"ccpool": {
			"actor": "a",
			"completion": "close-or-handback",
			"onFailure": "add-human",
			"onDispatchFail": "leave",
			"promptBody": "do work",
			"budget": {"time": "not-a-duration"}
		}
	}`)
	if _, err := loadRole(p); err == nil {
		t.Fatal("loadRole with an invalid budget.time must fail, got nil error")
	} else if !strings.Contains(err.Error(), "budget.time") {
		t.Errorf("loadRole error must name budget.time; got %v", err)
	}
}

// TestOverlayBudgetThresholds_ccpoolRoleGetsCfgThresholds proves
// overlayBudgetThresholds fills a ccpool role's Budget.Thresholds from the
// launch cfg's pool-wide Reminder/Cancel/HardPct, without disturbing the
// per-role Tokens/Cost/Time loadRole already decoded — without this, a role
// with any finite budget would evaluate against a Thresholds{0,0,0} zero
// value and hard-stop on its very first watchdog tick (see the function's
// own doc comment).
func TestOverlayBudgetThresholds_ccpoolRoleGetsCfgThresholds(t *testing.T) {
	role, err := loadRole(mustWriteRoleFile(t, budgetRoleTemplate))
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	cfg := config.Default()
	cfg.ReminderPct, cfg.CancelPct, cfg.HardPct = 0.5, 0.75, 0.95

	overlayBudgetThresholds(role, cfg)

	want := budget.Thresholds{Reminder: 0.5, Cancel: 0.75, Hard: 0.95}
	if role.CCPool.Budget.Thresholds != want {
		t.Errorf("Thresholds = %+v, want %+v", role.CCPool.Budget.Thresholds, want)
	}
	if role.CCPool.Budget.Tokens != 5000 || role.CCPool.Budget.Time != 25*time.Minute {
		t.Errorf("overlay must not disturb per-role Tokens/Time, got %+v", role.CCPool.Budget)
	}
}

// TestOverlayBudgetThresholds_commandRoleNoop proves overlayBudgetThresholds
// is a safe no-op for a command role (CCPool == nil), rather than panicking
// on a nil-pointer dereference.
func TestOverlayBudgetThresholds_commandRoleNoop(t *testing.T) {
	overlayBudgetThresholds(roles.Role{Name: "worker", Type: "command"}, config.Default())
}
