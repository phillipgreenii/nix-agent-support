package main

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

func TestDoctorHeader_poolContext(t *testing.T) {
	got := doctorPoolHeader(config.Config{PoolRoot: "/pools/alpha", DBPath: "/pools/alpha/store.db", StateDir: "/pools/alpha", Tmux: config.Tmux{Socket: "cc-abc123"}})
	for _, want := range []string{"/pools/alpha", "store.db", "cc-abc123", "hook.log"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor header missing %q:\n%s", want, got)
		}
	}
	def := doctorPoolHeader(config.Config{PoolRoot: "", DBPath: "/xdg/store.db", Tmux: config.Tmux{Socket: "ccpool"}})
	if !strings.Contains(def, "default") {
		t.Errorf("default mode header should say 'default':\n%s", def)
	}
}
