package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunTrust_writesTrustEntryForResolvedCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()

	if code := runTrust([]string{proj}); code != 0 {
		t.Fatalf("runTrust exit=%d, want 0", code)
	}

	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read ~/.claude.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The key must be the symlink-resolved path (matches what Claude records).
	resolved, _ := filepath.EvalSymlinks(proj)
	projects, _ := root["projects"].(map[string]any)
	p, _ := projects[resolved].(map[string]any)
	if p == nil || p["hasTrustDialogAccepted"] != true {
		t.Errorf("expected %q trusted; got projects=%v", resolved, projects)
	}
}
