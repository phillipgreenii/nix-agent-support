// internal/patheval/settings.go
package patheval

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SandboxFilesystemConfig holds path policy loaded from Claude Code settings.json.
// Field names match the sandbox.filesystem.* keys in settings.json.
// Arrays from multiple settings files are merged, matching Claude Code merge semantics.
type SandboxFilesystemConfig struct {
	DenyRead   []string `json:"denyRead"`
	DenyWrite  []string `json:"denyWrite"`
	AllowRead  []string `json:"allowRead"`
	AllowWrite []string `json:"allowWrite"`
}

type sandboxSettingsShape struct {
	Sandbox *struct {
		Filesystem *SandboxFilesystemConfig `json:"filesystem"`
	} `json:"sandbox"`
}

// LoadSandboxFilesystemConfig reads sandbox.filesystem.* from Claude Code settings
// files. Reads user settings (~/.claude/settings.json) then project-level
// (.claude/settings.json relative to projectDir), merging arrays from all sources.
// Missing files, missing keys, and parse errors are silently ignored.
// Returns an empty config (never nil) if nothing is configured.
func LoadSandboxFilesystemConfig(projectDir string) *SandboxFilesystemConfig {
	cfg := &SandboxFilesystemConfig{}
	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".claude", "settings.json"))
	}
	if projectDir != "" {
		candidates = append(candidates, filepath.Join(projectDir, ".claude", "settings.json"))
	}
	for _, path := range candidates {
		if partial := readSandboxFilesystem(path); partial != nil {
			cfg.DenyRead = append(cfg.DenyRead, partial.DenyRead...)
			cfg.DenyWrite = append(cfg.DenyWrite, partial.DenyWrite...)
			cfg.AllowRead = append(cfg.AllowRead, partial.AllowRead...)
			cfg.AllowWrite = append(cfg.AllowWrite, partial.AllowWrite...)
		}
	}
	return cfg
}

func readSandboxFilesystem(path string) *SandboxFilesystemConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s sandboxSettingsShape
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	if s.Sandbox == nil || s.Sandbox.Filesystem == nil {
		return nil
	}
	return s.Sandbox.Filesystem
}
