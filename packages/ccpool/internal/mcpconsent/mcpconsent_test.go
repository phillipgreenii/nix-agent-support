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
	if err := PreDisableUnclassified(dir, ""); err != nil {
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
	if err := PreDisableUnclassified(dir, ""); err != nil {
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
	if err := PreDisableUnclassified(dir, ""); err != nil {
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
	if err := PreDisableUnclassified(dir, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if err := PreDisableUnclassified(dir, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if string(before) != string(after) {
		t.Errorf("second run changed the file:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestPreDisableUnclassified_EmptyCanonicalPath_LegacyBehavior regression-pins
// that an empty canonicalSettingsPath is a pure no-op: identical to the
// pre-canonical-consultation behavior (default-deny every unclassified server,
// nothing consulted).
func TestPreDisableUnclassified_EmptyCanonicalPath_LegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"alpha": map[string]any{}, "gamma": map[string]any{}},
	})
	writeJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), map[string]any{
		"enabledMcpjsonServers": []any{"alpha"},
	})
	if err := PreDisableUnclassified(dir, ""); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["enabledMcpjsonServers"]), "alpha") {
		t.Errorf("alpha must stay enabled")
	}
	if !has(strList(s["disabledMcpjsonServers"]), "gamma") {
		t.Errorf("gamma must be default-disabled — canonicalSettingsPath is empty")
	}
}

// TestPreDisableUnclassified_CanonicalClassifiesUndeclaredServer_CopiedIn: a
// server the worktree has NOT classified, but the canonical file HAS
// classified (here: enabled), is copied into the worktree's settings instead of
// being default-disabled.
func TestPreDisableUnclassified_CanonicalClassifiesUndeclaredServer_CopiedIn(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"trusted": map[string]any{}},
	})
	canonical := filepath.Join(t.TempDir(), "canonical-settings.local.json")
	writeJSON(t, canonical, map[string]any{
		"enabledMcpjsonServers": []any{"trusted"},
	})
	if err := PreDisableUnclassified(dir, canonical); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["enabledMcpjsonServers"]), "trusted") {
		t.Errorf("enabled = %v, want [trusted] copied from the canonical file", s["enabledMcpjsonServers"])
	}
	if has(strList(s["disabledMcpjsonServers"]), "trusted") {
		t.Errorf("trusted must NOT be disabled — the canonical file classified it enabled")
	}
}

// TestPreDisableUnclassified_CanonicalDisabledRespected: the canonical file's
// DISABLED classification is copied in too, not just enabled — it must not be
// silently treated as "unclassified" and re-decided.
func TestPreDisableUnclassified_CanonicalDisabledRespected(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"untrusted": map[string]any{}},
	})
	canonical := filepath.Join(t.TempDir(), "canonical-settings.local.json")
	writeJSON(t, canonical, map[string]any{
		"disabledMcpjsonServers": []any{"untrusted"},
	})
	if err := PreDisableUnclassified(dir, canonical); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["disabledMcpjsonServers"]), "untrusted") {
		t.Errorf("disabled = %v, want [untrusted] copied from the canonical file", s["disabledMcpjsonServers"])
	}
}

// TestPreDisableUnclassified_WorktreeClassificationWinsOverCanonical: a server
// the worktree has ALREADY classified must not be overwritten by a conflicting
// canonical classification — the worktree's own settings.local.json is
// authoritative for anything it already decided.
func TestPreDisableUnclassified_WorktreeClassificationWinsOverCanonical(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"shared": map[string]any{}},
	})
	writeJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), map[string]any{
		"disabledMcpjsonServers": []any{"shared"},
	})
	canonical := filepath.Join(t.TempDir(), "canonical-settings.local.json")
	writeJSON(t, canonical, map[string]any{
		"enabledMcpjsonServers": []any{"shared"}, // conflicts with the worktree's own decision
	})
	if err := PreDisableUnclassified(dir, canonical); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if has(strList(s["enabledMcpjsonServers"]), "shared") {
		t.Errorf("the worktree's own disabled classification must win over the canonical file")
	}
	if !has(strList(s["disabledMcpjsonServers"]), "shared") {
		t.Errorf("shared must stay disabled per the worktree's own settings")
	}
}

// TestPreDisableUnclassified_StillUnclassifiedInBoth_Disabled: a server neither
// the worktree nor the canonical file classifies still falls through to
// default-deny.
func TestPreDisableUnclassified_StillUnclassifiedInBoth_Disabled(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"mystery": map[string]any{}},
	})
	canonical := filepath.Join(t.TempDir(), "canonical-settings.local.json")
	writeJSON(t, canonical, map[string]any{
		"enabledMcpjsonServers": []any{"unrelated-server"},
	})
	if err := PreDisableUnclassified(dir, canonical); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["disabledMcpjsonServers"]), "mystery") {
		t.Errorf("mystery must be default-disabled — unclassified in both files")
	}
}

// TestPreDisableUnclassified_CanonicalFileMissing_FallsBackCleanly: a
// canonicalSettingsPath pointing at a nonexistent file must not error — it
// falls back to pure default-deny.
func TestPreDisableUnclassified_CanonicalFileMissing_FallsBackCleanly(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"solo": map[string]any{}},
	})
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	if err := PreDisableUnclassified(dir, missing); err != nil {
		t.Fatalf("missing canonical file must not error, got: %v", err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["disabledMcpjsonServers"]), "solo") {
		t.Errorf("solo must be default-disabled when the canonical file is missing")
	}
}

// TestPreDisableUnclassified_CanonicalFileGarbage_FallsBackCleanly: an
// unparseable canonical file must not error either — same fallback.
func TestPreDisableUnclassified_CanonicalFileGarbage_FallsBackCleanly(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{"solo": map[string]any{}},
	})
	garbage := filepath.Join(t.TempDir(), "garbage.json")
	if err := os.MkdirAll(filepath.Dir(garbage), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(garbage, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PreDisableUnclassified(dir, garbage); err != nil {
		t.Fatalf("garbage canonical file must not error, got: %v", err)
	}
	s := readSettings(t, dir)
	if !has(strList(s["disabledMcpjsonServers"]), "solo") {
		t.Errorf("solo must be default-disabled when the canonical file is unparseable")
	}
}
