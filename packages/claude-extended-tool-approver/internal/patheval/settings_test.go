// internal/patheval/settings_test.go
package patheval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSandboxFilesystemConfig_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg := LoadSandboxFilesystemConfig(dir)
	if len(cfg.DenyRead) != 0 || len(cfg.DenyWrite) != 0 ||
		len(cfg.AllowRead) != 0 || len(cfg.AllowWrite) != 0 {
		t.Errorf("expected empty config for missing files, got %+v", cfg)
	}
}

func TestLoadSandboxFilesystemConfig_UserSettings(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{
		"sandbox": map[string]any{
			"filesystem": map[string]any{
				"denyRead":   []string{"/home/user/.ssh"},
				"denyWrite":  []string{"/home/user/.gnupg"},
				"allowWrite": []string{"/home/user/.cache"},
			},
		},
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)
	cfg := LoadSandboxFilesystemConfig("")
	if len(cfg.DenyRead) != 1 || cfg.DenyRead[0] != "/home/user/.ssh" {
		t.Errorf("DenyRead = %v, want [/home/user/.ssh]", cfg.DenyRead)
	}
	if len(cfg.DenyWrite) != 1 || cfg.DenyWrite[0] != "/home/user/.gnupg" {
		t.Errorf("DenyWrite = %v, want [/home/user/.gnupg]", cfg.DenyWrite)
	}
	if len(cfg.AllowWrite) != 1 || cfg.AllowWrite[0] != "/home/user/.cache" {
		t.Errorf("AllowWrite = %v, want [/home/user/.cache]", cfg.AllowWrite)
	}
}

func TestLoadSandboxFilesystemConfig_MergesProjectSettings(t *testing.T) {
	homeDir := t.TempDir()
	projectDir := t.TempDir()

	homeClaudeDir := filepath.Join(homeDir, ".claude")
	projectClaudeDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(homeClaudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectClaudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	userSettings := map[string]any{
		"sandbox": map[string]any{
			"filesystem": map[string]any{
				"denyRead": []string{"/home/user/.ssh"},
			},
		},
	}
	projectSettings := map[string]any{
		"sandbox": map[string]any{
			"filesystem": map[string]any{
				"denyRead": []string{"/home/user/.gnupg"},
			},
		},
	}

	data, err := json.Marshal(userSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeClaudeDir, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(projectSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectClaudeDir, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", homeDir)
	cfg := LoadSandboxFilesystemConfig(projectDir)
	if len(cfg.DenyRead) != 2 {
		t.Fatalf("DenyRead = %v, want 2 entries (merged from user+project)", cfg.DenyRead)
	}
	if cfg.DenyRead[0] != "/home/user/.ssh" {
		t.Errorf("DenyRead[0] = %q, want /home/user/.ssh (user settings first)", cfg.DenyRead[0])
	}
	if cfg.DenyRead[1] != "/home/user/.gnupg" {
		t.Errorf("DenyRead[1] = %q, want /home/user/.gnupg (project settings second)", cfg.DenyRead[1])
	}
}

func TestLoadSandboxFilesystemConfig_MissingSandboxKey(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"statusLine": {}}`), 0644)

	t.Setenv("HOME", dir)
	cfg := LoadSandboxFilesystemConfig("")
	if len(cfg.DenyRead) != 0 {
		t.Errorf("expected empty DenyRead for settings without sandbox key, got %v", cfg.DenyRead)
	}
}
