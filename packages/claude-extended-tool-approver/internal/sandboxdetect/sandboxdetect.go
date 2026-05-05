// Package sandboxdetect determines whether Claude Code's bash sandbox is
// enabled for the current invocation by inspecting the merged settings files.
//
// This is best-effort telemetry: it reflects *configured* state, not
// *effective* state. On Linux the sandbox additionally requires bubblewrap
// and socat to be installed; this package does not check for those. Today
// it is intended for macOS, where Seatbelt is always available when the
// setting is enabled.
package sandboxdetect

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// settingsShape is the minimal slice of settings.json we care about.
// Fields use *bool so we can distinguish "unset" from "explicitly false".
type settingsShape struct {
	Sandbox *struct {
		Enabled *bool `json:"enabled"`
	} `json:"sandbox"`
}

// Detect returns true if the merged Claude Code settings have
// sandbox.enabled = true. Precedence (later wins):
//
//	1. ~/.claude/settings.json
//	2. <projectDir>/.claude/settings.json
//	3. <projectDir>/.claude/settings.local.json
//
// projectDir should be CLAUDE_PROJECT_DIR (or the hook's CWD as a fallback).
// Missing files, parse errors, and unset fields are all treated as "no
// opinion" — they do not flip the result.
func Detect(projectDir string) bool {
	enabled := false

	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".claude", "settings.json"))
	}
	if projectDir != "" {
		candidates = append(candidates,
			filepath.Join(projectDir, ".claude", "settings.json"),
			filepath.Join(projectDir, ".claude", "settings.local.json"),
		)
	}

	for _, path := range candidates {
		if v, ok := readSandboxEnabled(path); ok {
			enabled = v
		}
	}
	return enabled
}

// readSandboxEnabled returns (value, true) if the file exists, parses, and
// has sandbox.enabled set. Returns (false, false) otherwise.
func readSandboxEnabled(path string) (bool, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var s settingsShape
	if err := json.Unmarshal(data, &s); err != nil {
		return false, false
	}
	if s.Sandbox == nil || s.Sandbox.Enabled == nil {
		return false, false
	}
	return *s.Sandbox.Enabled, true
}
