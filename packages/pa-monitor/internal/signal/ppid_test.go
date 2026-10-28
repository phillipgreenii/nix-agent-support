package signal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// TestParentPID_ParsesPsOutput drives ParentPID through a recording ps shim
// first on PATH (hermetic; never the host's ps).
func TestParentPID_ParsesPsOutput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "ps.log")
	shim := "#!/bin/sh\necho \"$@\" >> '" + log + "'\nif [ \"$4\" = 77 ]; then echo '  4242'; else echo; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got, ok := signal.ParentPID(77); !ok || got != 4242 {
		t.Fatalf("ParentPID(77) = %d, %v; want 4242, true", got, ok)
	}
	if got, ok := signal.ParentPID(78); ok {
		t.Fatalf("ParentPID(78) = %d, true; want ok=false for empty output", got)
	}
	b, _ := os.ReadFile(log)
	if want := "-o ppid= -p 77\n-o ppid= -p 78\n"; string(b) != want {
		t.Fatalf("shim recorded %q, want %q", b, want)
	}
}
