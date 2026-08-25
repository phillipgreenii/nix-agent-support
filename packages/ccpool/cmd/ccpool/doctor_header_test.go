package main

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

// TestDoctorHeader_poolContext is a pure unit test of doctorPoolHeader (string
// formatting only, no subprocess/tmux) — split out of pool_integration_test.go
// (bead pg2-h05lt) so it keeps running in the unit tier alongside the
// `//go:build integration`-tagged TestPools_isolated left there.
func TestDoctorHeader_poolContext(t *testing.T) {
	got := doctorPoolHeader(config.Config{PoolRoot: "/pools/alpha", DBPath: "/pools/alpha/store.db", StateDir: "/pools/alpha", Tmux: config.Tmux{Socket: "cc-abc123"}})
	for _, want := range []string{"/pools/alpha", "store.db", "cc-abc123", "diagnostics.jsonl", "events.jsonl"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor header missing %q:\n%s", want, got)
		}
	}
	def := doctorPoolHeader(config.Config{PoolRoot: "", DBPath: "/xdg/store.db", Tmux: config.Tmux{Socket: "ccpool"}})
	if !strings.Contains(def, "default") {
		t.Errorf("default mode header should say 'default':\n%s", def)
	}
}
