package mcpconsent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return m
}

func strList(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func TestPreDisableUnclassified_NoMcpJson_NoOp(t *testing.T) {
	dir := t.TempDir()
	if err := PreDisableUnclassified(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.local.json")); !os.IsNotExist(err) {
		t.Errorf("settings.local.json should not be created when no .mcp.json exists")
	}
}

func TestPreDisableUnclassified_DisablesOnlyUnclassified(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"alpha": map[string]any{}, "beta": map[string]any{}, "gamma": map[string]any{}},
	})
	// alpha is already enabled, beta already disabled — must be preserved;
	// gamma is unclassified → gets disabled.
	writeJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), map[string]any{
		"enabledMcpjsonServers":  []any{"alpha"},
		"disabledMcpjsonServers": []any{"beta"},
		"permissions":            map[string]any{"allow": []any{"Bash(ls:*)"}},
	})
	if err := PreDisableUnclassified(dir); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	enabled := strList(s["enabledMcpjsonServers"])
	disabled := strList(s["disabledMcpjsonServers"])
	if !has(enabled, "alpha") || len(enabled) != 1 {
		t.Errorf("enabled = %v, want [alpha]", enabled)
	}
	if !has(disabled, "beta") || !has(disabled, "gamma") || len(disabled) != 2 {
		t.Errorf("disabled = %v, want [beta gamma]", disabled)
	}
	if has(disabled, "alpha") {
		t.Errorf("alpha must NOT be disabled (it was explicitly enabled)")
	}
	// Unrelated keys preserved.
	if _, ok := s["permissions"]; !ok {
		t.Errorf("permissions key was dropped")
	}
}

func TestPreDisableUnclassified_CreatesSettingsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"one": map[string]any{}, "two": map[string]any{}},
	})
	if err := PreDisableUnclassified(dir); err != nil {
		t.Fatal(err)
	}
	disabled := strList(readSettings(t, dir)["disabledMcpjsonServers"])
	if !has(disabled, "one") || !has(disabled, "two") || len(disabled) != 2 {
		t.Errorf("disabled = %v, want [one two]", disabled)
	}
}

func TestPreDisableUnclassified_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"x": map[string]any{}},
	})
	if err := PreDisableUnclassified(dir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if err := PreDisableUnclassified(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if string(before) != string(after) {
		t.Errorf("second run changed the file:\nbefore=%s\nafter=%s", before, after)
	}
}
