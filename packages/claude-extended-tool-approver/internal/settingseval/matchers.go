package settingseval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// parseMatcher creates the appropriate matcher for a permission rule string.
func parseMatcher(raw, settingsDir string) matcher {
	// Bash(cmd:*) — prefix match on command
	if strings.HasPrefix(raw, "Bash(") && strings.HasSuffix(raw, ")") {
		inner := raw[5 : len(raw)-1]
		prefix := strings.TrimSuffix(inner, ":*")
		return &bashMatcher{prefix: prefix}
	}

	// File tool with path: Read(./**), Write(//nix/store/**), etc.
	fileTools := []string{"Read", "Write", "Delete", "StrReplace", "Glob", "Grep", "ReadLints", "Edit", "MultiEdit"}
	for _, tool := range fileTools {
		if strings.HasPrefix(raw, tool+"(") && strings.HasSuffix(raw, ")") {
			inner := raw[len(tool)+1 : len(raw)-1]
			return &pathMatcher{toolName: tool, pattern: inner, settingsDir: settingsDir}
		}
	}

	// WebFetch(domain:x) — domain suffix match
	if strings.HasPrefix(raw, "WebFetch(domain:") && strings.HasSuffix(raw, ")") {
		domain := raw[len("WebFetch(domain:") : len(raw)-1]
		return &webFetchMatcher{domain: domain}
	}

	// mcp__server__tool — exact match on tool_name
	if strings.HasPrefix(raw, "mcp__") {
		return &exactMatcher{toolName: raw}
	}

	// Bare tool name — exact match
	return &exactMatcher{toolName: raw}
}

// bashMatcher implements prefix matching for Bash commands.
type bashMatcher struct {
	prefix string
}

func (m *bashMatcher) matches(toolName string, toolInput json.RawMessage, _ string) bool {
	if toolName != "Bash" {
		return false
	}
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return false
	}
	return strings.HasPrefix(input.Command, m.prefix)
}

// pathMatcher implements gitignore-style path glob matching for file tools.
type pathMatcher struct {
	toolName    string
	pattern     string
	settingsDir string
}

func (m *pathMatcher) matches(toolName string, toolInput json.RawMessage, cwd string) bool {
	if toolName != m.toolName {
		return false
	}
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return false
	}
	return m.matchPath(input.FilePath, cwd)
}

func (m *pathMatcher) matchPath(filePath, cwd string) bool {
	// Resolve the pattern to an absolute glob
	absPattern := m.resolvePattern(cwd)

	// Resolve the file path to absolute
	absFile := filePath
	if !filepath.IsAbs(absFile) {
		absFile = filepath.Join(cwd, absFile)
	}
	absFile = filepath.Clean(absFile)

	matched, err := filepath.Match(absPattern, absFile)
	if err == nil && matched {
		return true
	}

	// For ** patterns, do a prefix check
	if strings.HasSuffix(absPattern, "/**") {
		dir := strings.TrimSuffix(absPattern, "/**")
		return strings.HasPrefix(absFile, dir+"/") || absFile == dir
	}

	return false
}

func (m *pathMatcher) resolvePattern(cwd string) string {
	p := m.pattern

	// ./** or ./path — relative to CWD
	if strings.HasPrefix(p, "./") {
		return filepath.Join(cwd, p[2:])
	}

	// ~/** — relative to home directory
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}

	// //path — absolute filesystem path (double slash = absolute)
	if strings.HasPrefix(p, "//") {
		return p[1:] // Strip one leading slash
	}

	// /path — absolute filesystem path
	// Claude Code treats paths starting with / as absolute.
	if strings.HasPrefix(p, "/") {
		return p
	}

	// No prefix — relative to CWD
	return filepath.Join(cwd, p)
}

// webFetchMatcher implements domain suffix matching for WebFetch.
type webFetchMatcher struct {
	domain string
}

func (m *webFetchMatcher) matches(toolName string, toolInput json.RawMessage, _ string) bool {
	if toolName != "WebFetch" {
		return false
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return false
	}
	// Extract domain from URL
	url := input.URL
	if idx := strings.Index(url, "://"); idx >= 0 {
		url = url[idx+3:]
	}
	if idx := strings.Index(url, "/"); idx >= 0 {
		url = url[:idx]
	}
	if idx := strings.Index(url, ":"); idx >= 0 {
		url = url[:idx]
	}
	return url == m.domain || strings.HasSuffix(url, "."+m.domain)
}

// exactMatcher implements exact match on tool name.
type exactMatcher struct {
	toolName string
}

func (m *exactMatcher) matches(toolName string, _ json.RawMessage, _ string) bool {
	return toolName == m.toolName
}
