package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readProjects(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	p, _ := root["projects"].(map[string]any)
	return p
}

func TestEnsureTrusted_addsFlag_preservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"someTopLevelKey": "keepme",
		"projects": map[string]any{
			"/other": map[string]any{"hasTrustDialogAccepted": true, "lastCost": 1.5},
		},
	})

	if err := EnsureTrusted(path, "/tmp/proj"); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}

	// Preserved top-level + unrelated project.
	b, _ := os.ReadFile(path)
	var root map[string]any
	_ = json.Unmarshal(b, &root)
	if root["someTopLevelKey"] != "keepme" {
		t.Error("top-level key not preserved")
	}
	projs := readProjects(t, path)
	if other, _ := projs["/other"].(map[string]any); other["lastCost"] != 1.5 {
		t.Error("unrelated project's keys not preserved")
	}
	// New project trusted.
	proj, ok := projs["/tmp/proj"].(map[string]any)
	if !ok || proj["hasTrustDialogAccepted"] != true {
		t.Errorf("/tmp/proj not trusted: %v", projs["/tmp/proj"])
	}
}

func TestEnsureTrusted_noFileCreatesOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := EnsureTrusted(path, "/tmp/proj"); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	projs := readProjects(t, path)
	if proj, _ := projs["/tmp/proj"].(map[string]any); proj["hasTrustDialogAccepted"] != true {
		t.Error("project not trusted when file absent")
	}
}

func TestEnsureTrusted_idempotentWhenAlreadyTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"projects": map[string]any{"/tmp/proj": map[string]any{"hasTrustDialogAccepted": true}},
	})
	before, _ := os.ReadFile(path)
	if err := EnsureTrusted(path, "/tmp/proj"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("already-trusted project should be a no-op (no rewrite)")
	}
}
