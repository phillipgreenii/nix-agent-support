package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
