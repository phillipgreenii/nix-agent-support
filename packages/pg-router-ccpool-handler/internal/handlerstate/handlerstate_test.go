package handlerstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
)

func TestDir_prefersConfiguredStateDir(t *testing.T) {
	cfg := config.Default()
	cfg.OriginProbe.StateDir = "/custom/state"
	if got := Dir(cfg); got != "/custom/state" {
		t.Errorf("Dir = %q, want /custom/state", got)
	}
}

func TestNewEventLog_opensEventsJSONLUnderStateDir(t *testing.T) {
	cfg := config.Default()
	cfg.OriginProbe.StateDir = t.TempDir()
	w := NewEventLog(cfg)
	if w == nil {
		t.Fatal("NewEventLog returned nil")
	}
	if err := w.Emit("info", "probe", "hello", nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OriginProbe.StateDir, EventLogName)); err != nil {
		t.Errorf("event log file missing: %v", err)
	}
}
