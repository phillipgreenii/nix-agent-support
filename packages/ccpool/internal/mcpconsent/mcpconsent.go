// Package mcpconsent pre-records MCP-server consent for a worktree so an
// automated `claude` launch does not stall on the interactive "New MCP server
// found in this project" prompt — which nothing in a headless pool worker can
// answer (pg2-80ji). Claude's MCP consent is EXACT-match on the server name (no
// wildcard), so a durable "reject all unknown servers" pre-record must enumerate
// every server declared in the worktree's .mcp.json and default each
// UNCLASSIFIED one into disabledMcpjsonServers, least-privilege. Servers the
// user has already classified (in enabledMcpjsonServers or disabledMcpjsonServers)
// are left untouched.
//
// PreDisableUnclassified MAY optionally consult a read-only "canonical decisions"
// settings file first, copying in any classification a human already made
// elsewhere before falling back to default-deny for whatever remains
// unclassified (docs/adr/0052-ccpool-mcp-consent-canonical-decisions-consultation.md
// in phillipgreenii-nix-agent-support).
package mcpconsent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PreDisableUnclassified reads <worktreeDir>/.mcp.json and, for every declared
// server not already listed in either enabledMcpjsonServers or
// disabledMcpjsonServers of <worktreeDir>/.claude/settings.local.json, appends
// it to disabledMcpjsonServers. It is a no-op (no file created) when the
// worktree has no .mcp.json or declares no servers, and it rewrites
// settings.local.json only when it actually adds a server (so re-runs are
// idempotent and the Claude-managed file is touched minimally). All other keys
// in settings.local.json are preserved.
//
// When canonicalSettingsPath is non-empty, it names a settings.local.json-shaped
// file consulted READ-ONLY (never written) before the default-deny step: any
// server declared in .mcp.json that canonicalSettingsPath already classifies,
// but the worktree's own settings.local.json does not yet classify, has that
// classification copied into the worktree's file. Only servers still
// unclassified after that consultation fall through to default-deny. An empty
// canonicalSettingsPath is a pure no-op (identical to the pre-consultation
// behavior). A missing, unreadable, or unparseable canonical file is never a
// hard error — consultation is best-effort and silently falls back to
// default-deny for every server, so the headless-safety property (every
// unclassified server ends up denied) can never regress because of a bad
// canonical file.
func PreDisableUnclassified(worktreeDir, canonicalSettingsPath string) error {
	servers, err := readMcpServerNames(filepath.Join(worktreeDir, ".mcp.json"))
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}

	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.local.json")
	root, err := readSettingsFile(settingsPath)
	if err != nil {
		return err
	}

	enabledOrig := stringSlice(root["enabledMcpjsonServers"])
	disabledOrig := stringSlice(root["disabledMcpjsonServers"])
	enabled := append([]string(nil), enabledOrig...)
	disabled := append([]string(nil), disabledOrig...)
	classified := classifiedSet(enabled, disabled)

	// Best-effort read-only consultation: copy in any classification the
	// canonical file already made for a server this worktree hasn't classified
	// yet. Any failure reading/parsing it is swallowed — consultation never
	// blocks or corrupts the worktree's own settings, and every server it
	// couldn't resolve simply falls through to default-deny below.
	if canonicalSettingsPath != "" {
		if canon, ok := tryReadSettingsFile(canonicalSettingsPath); ok {
			canonEnabled := stringSlice(canon["enabledMcpjsonServers"])
			canonDisabled := stringSlice(canon["disabledMcpjsonServers"])
			canonClassified := classifiedSet(canonEnabled, canonDisabled)
			canonIsEnabled := map[string]bool{}
			for _, s := range canonEnabled {
				canonIsEnabled[s] = true
			}
			for _, name := range servers {
				if classified[name] || !canonClassified[name] {
					continue
				}
				if canonIsEnabled[name] {
					enabled = append(enabled, name)
				} else {
					disabled = append(disabled, name)
				}
				classified[name] = true
			}
		}
	}

	for _, name := range servers {
		if !classified[name] {
			disabled = append(disabled, name)
			classified[name] = true
		}
	}

	enabledChanged := len(enabled) != len(enabledOrig)
	disabledChanged := len(disabled) != len(disabledOrig)
	if !enabledChanged && !disabledChanged {
		return nil // nothing unclassified and nothing copied — leave the Claude-managed file alone
	}

	if enabledChanged {
		root["enabledMcpjsonServers"] = enabled
	}
	if disabledChanged {
		root["disabledMcpjsonServers"] = disabled
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(settingsPath), err)
	}
	return atomicWriteJSON(settingsPath, root)
}

// readSettingsFile reads a settings.local.json-shaped file at path, returning an
// empty map for a missing file. A read or parse error on an EXISTING file is
// returned to the caller (used for the worktree's own settings file, where a
// parse failure should surface rather than be silently ignored).
func readSettingsFile(path string) (map[string]any, error) {
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return root, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, nil
}

// tryReadSettingsFile is the read-only, best-effort counterpart of
// readSettingsFile used for the canonical-decisions file: any failure (missing,
// unreadable, or unparseable) reports ok=false instead of an error, since
// canonical-file consultation MUST NOT be a hard error (docs/adr/0052).
func tryReadSettingsFile(path string) (map[string]any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, false
	}
	return root, true
}

// classifiedSet builds the set of server names already classified by either
// list (enabled or disabled).
func classifiedSet(enabled, disabled []string) map[string]bool {
	classified := make(map[string]bool, len(enabled)+len(disabled))
	for _, s := range enabled {
		classified[s] = true
	}
	for _, s := range disabled {
		classified[s] = true
	}
	return classified
}

// readMcpServerNames returns the server names declared under .mcpServers in the
// .mcp.json at path. A missing file yields no names (nil) and no error.
func readMcpServerNames(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	names := make([]string, 0, len(doc.McpServers))
	for name := range doc.McpServers {
		names = append(names, name)
	}
	return names, nil
}

func stringSlice(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.local.json.tmp-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
