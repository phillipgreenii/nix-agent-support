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
}
