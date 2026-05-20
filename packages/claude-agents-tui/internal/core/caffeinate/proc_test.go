package caffeinate

import (
	"runtime"
	"testing"
)

func TestProcSpawnKillRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("caffeinate is macOS only")
	}
	p := &Proc{}
	if err := p.Spawn(123); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("double Kill: %v", err)
	}
}
