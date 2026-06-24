package config

import "testing"

// TestExampleTOML_roundTrips guarantees the generated example config actually loads
// and reproduces the built-in feedback + worker roles — so 'config --print-defaults'
// output is always a valid, copy-pasteable starting point.
func TestExampleTOML_roundTrips(t *testing.T) {
	writeCfg(t, ExampleTOML())
	c, err := Load()
	if err != nil {
		t.Fatalf("example config must load: %v\n---\n%s", err, ExampleTOML())
	}
	if len(c.Roles) != 2 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" {
		t.Fatalf("example must reproduce built-in feedback+worker: %+v", c.Roles)
	}
	// The worker's authorship guard and completion mode must survive the round-trip.
	if c.Roles[1].CCPool == nil || !c.Roles[1].CCPool.AuthorshipGuard || c.Roles[1].CCPool.Completion != "close-or-handback" {
		t.Fatalf("worker ccpool config did not round-trip: %+v", c.Roles[1].CCPool)
	}
	// The feedback role carries budget.Budget{} (fully unlimited => NO watchdog) in
	// roles.BuiltinRoleSet. Without an emitted [role.ccpool.budget] table, buildCCPool
	// seeds it from the pool default (Time=25m) and the example reload silently adds a
	// 25m watchdog. Assert the exact "fully unlimited" triple executor.budgetUnlimited
	// checks: Tokens<=0 && Cost<=0 && Time<=0.
	fb := c.Roles[0].CCPool
	if fb == nil {
		t.Fatalf("feedback ccpool config missing after round-trip: %+v", c.Roles[0])
	}
	if !fb.Budget.Tokens.Unlimited() || !fb.Budget.Cost.Unlimited() || fb.Budget.Time > 0 {
		t.Fatalf("feedback budget did not round-trip to unlimited (got Tokens=%d Cost=%d Time=%v); "+
			"print-defaults reload added a watchdog", int64(fb.Budget.Tokens), int64(fb.Budget.Cost), fb.Budget.Time)
	}
}
