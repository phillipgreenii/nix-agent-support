// Package mcpconsent pre-records MCP-server consent for a worktree so an
// automated `claude` launch does not stall on the interactive "New MCP server
// found in this project" prompt — which nothing in a headless pool worker can
// answer (pg2-80ji). Claude's MCP consent is EXACT-match on the server name (no
// wildcard), so a durable "reject all unknown servers" pre-record must enumerate
// every server declared in the worktree's .mcp.json and default each
// UNCLASSIFIED one into disabledMcpjsonServers, least-privilege. Servers the
// user has already classified (in enabledMcpjsonServers or disabledMcpjsonServers)
// are left untouched.
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
func PreDisableUnclassified(worktreeDir string) error {
	servers, err := readMcpServerNames(filepath.Join(worktreeDir, ".mcp.json"))
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}

	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.local.json")
	root := map[string]any{}
	if b, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	classified := map[string]bool{}
	for _, s := range stringSlice(root["enabledMcpjsonServers"]) {
		classified[s] = true
	}
	disabled := stringSlice(root["disabledMcpjsonServers"])
	for _, s := range disabled {
		classified[s] = true
	}

	added := false
	for _, name := range servers {
		if !classified[name] {
			disabled = append(disabled, name)
			classified[name] = true
			added = true
		}
	}
	if !added {
		return nil // nothing unclassified — leave the Claude-managed file alone
	}

	root["disabledMcpjsonServers"] = disabled
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(settingsPath), err)
	}
	return atomicWriteJSON(settingsPath, root)
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
