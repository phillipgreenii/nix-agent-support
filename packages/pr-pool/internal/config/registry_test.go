package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDecodeGlobalBudget_overlaysBudgetOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := "[pool]\nself_login = \"ignored\"\n[pool.budget]\ntokens = 500000\ntime = \"40m\"\n" +
		"[[role]]\nname = \"ignored-role\"\ntype = \"command\"\n[role.command]\nargv = [\"x\"]\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err != nil {
		t.Fatal(err)
	}
	if c.BudgetTokens != 500000 {
		t.Errorf("BudgetTokens = %d, want 500000", c.BudgetTokens)
	}
	if c.BudgetTime != 40*time.Minute {
		t.Errorf("BudgetTime = %v, want 40m", c.BudgetTime)
	}
	// Cost omitted in file => Default() (0/unlimited) preserved.
	if c.BudgetCost != 0 {
		t.Errorf("BudgetCost = %d, want 0 (unchanged)", c.BudgetCost)
	}
	// self_login and [[role]] must be IGNORED by the global layer (budget-only scope).
	if c.SelfLogin != "" {
		t.Errorf("SelfLogin = %q, want empty (global file must not set non-budget scalars)", c.SelfLogin)
	}
}

func TestDecodeGlobalBudget_malformedIsHardError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("this is = not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err == nil {
		t.Fatal("malformed global config must be a hard error")
	}
}
