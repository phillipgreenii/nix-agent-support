package settingseval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

func TestNewSettingsEvaluator_ParsesFile(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git log:*)", "WebSearch"],
			"deny": ["Bash(rm -rf:*)"],
			"ask": ["Bash(git push --force:*)"]
		}
	}`)

	se, err := NewSettingsEvaluator(path)
	if err != nil {
		t.Fatalf("NewSettingsEvaluator: %v", err)
	}
	if len(se.allow) != 2 {
		t.Errorf("allow rules = %d, want 2", len(se.allow))
	}
	if len(se.deny) != 1 {
		t.Errorf("deny rules = %d, want 1", len(se.deny))
	}
	if len(se.ask) != 1 {
		t.Errorf("ask rules = %d, want 1", len(se.ask))
	}
}

func TestEvaluate_BashPrefixMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git log:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	tests := []struct {
		cmd  string
		want string
	}{
		{"git log --oneline", "allow"},
		{"git log", "allow"},
		{"git status", ""},
		{"gitlog", ""},
	}

	for _, tt := range tests {
		got := se.Evaluate("Bash", json.RawMessage(`{"command":"`+tt.cmd+`"}`), "/tmp")
		if got != tt.want {
			t.Errorf("Bash(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestEvaluate_BashWithoutWildcard(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(echo:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"echo hello"}`), "/tmp"); got != "allow" {
		t.Errorf("echo hello = %q, want allow", got)
	}
}

func TestEvaluate_ExactToolName(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["AskQuestion", "WebSearch", "Skill"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	for _, tool := range []string{"AskQuestion", "WebSearch", "Skill"} {
		if got := se.Evaluate(tool, json.RawMessage(`{}`), "/tmp"); got != "allow" {
			t.Errorf("%s = %q, want allow", tool, got)
		}
	}
	if got := se.Evaluate("Unknown", json.RawMessage(`{}`), "/tmp"); got != "" {
		t.Errorf("Unknown = %q, want empty", got)
	}
}

func TestEvaluate_MCPExactMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["mcp__Atlassian-MCP-Server__getJiraIssue", "mcp__Notion__notion-fetch"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("mcp__Atlassian-MCP-Server__getJiraIssue", json.RawMessage(`{}`), "/tmp"); got != "allow" {
		t.Errorf("MCP match = %q, want allow", got)
	}
	if got := se.Evaluate("mcp__Atlassian-MCP-Server__other", json.RawMessage(`{}`), "/tmp"); got != "" {
		t.Errorf("MCP non-match = %q, want empty", got)
	}
}

func TestEvaluate_PathMatch_CWDRelative(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Read(./**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	cwd := "/projects/myapp"

	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"/projects/myapp/src/main.go"}`), cwd); got != "allow" {
		t.Errorf("Read cwd-relative = %q, want allow", got)
	}
	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"/other/path/file.go"}`), cwd); got != "" {
		t.Errorf("Read outside cwd = %q, want empty", got)
	}
}

func TestEvaluate_PathMatch_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Read(//nix/store/**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"/nix/store/abc123-pkg/bin/tool"}`), "/tmp"); got != "allow" {
		t.Errorf("Read nix store = %q, want allow", got)
	}
	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"/other/path"}`), "/tmp"); got != "" {
		t.Errorf("Read non-nix = %q, want empty", got)
	}
}

func TestEvaluate_PathMatch_HomeRelative(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Read(~/.claude/plugins/**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	home, _ := os.UserHomeDir()
	pluginPath := filepath.Join(home, ".claude/plugins/my-plugin/SKILL.md")

	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"`+pluginPath+`"}`), "/tmp"); got != "allow" {
		t.Errorf("Read home-relative = %q, want allow", got)
	}
}

func TestEvaluate_PathMatch_WriteAndDelete(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Write(./**)", "Delete(./**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	cwd := "/projects/myapp"
	if got := se.Evaluate("Write", json.RawMessage(`{"file_path":"/projects/myapp/output.txt"}`), cwd); got != "allow" {
		t.Errorf("Write cwd-relative = %q, want allow", got)
	}
	if got := se.Evaluate("Delete", json.RawMessage(`{"file_path":"/projects/myapp/temp.txt"}`), cwd); got != "allow" {
		t.Errorf("Delete cwd-relative = %q, want allow", got)
	}
}

func TestEvaluate_WebFetchDomain(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["WebFetch(domain:example.com)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/page", "allow"},
		{"https://sub.example.com/page", "allow"},
		{"https://notexample.com/page", ""},
		{"https://evil.com/page", ""},
	}

	for _, tt := range tests {
		got := se.Evaluate("WebFetch", json.RawMessage(`{"url":"`+tt.url+`"}`), "/tmp")
		if got != tt.want {
			t.Errorf("WebFetch(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestEvaluate_Precedence_DenyOverAllow(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git:*)"],
			"deny": ["Bash(git push --force:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"git log"}`), "/tmp"); got != "allow" {
		t.Errorf("git log = %q, want allow", got)
	}
	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"git push --force"}`), "/tmp"); got != "deny" {
		t.Errorf("git push --force = %q, want deny", got)
	}
}

func TestEvaluate_Precedence_AskOverAllow(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git:*)"],
			"ask": ["Bash(git push:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"git log"}`), "/tmp"); got != "allow" {
		t.Errorf("git log = %q, want allow", got)
	}
	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"git push origin main"}`), "/tmp"); got != "ask" {
		t.Errorf("git push = %q, want ask", got)
	}
}

func TestEvaluate_NoPermissions(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"permissions": {}}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Bash", json.RawMessage(`{"command":"ls"}`), "/tmp"); got != "" {
		t.Errorf("no permissions = %q, want empty", got)
	}
}

func TestRules_ReturnsRawStrings(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git log:*)", "WebSearch"],
			"deny": ["Bash(rm:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	allow := se.Rules("allow")
	if len(allow) != 2 {
		t.Errorf("allow rules = %d, want 2", len(allow))
	}
	deny := se.Rules("deny")
	if len(deny) != 1 {
		t.Errorf("deny rules = %d, want 1", len(deny))
	}
	if got := se.Rules("unknown"); got != nil {
		t.Errorf("unknown rules = %v, want nil", got)
	}
}

func TestEvaluate_NonBashToolNotMatchedByBashRule(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Bash(git log:*)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Read", json.RawMessage(`{"file_path":"/tmp/git log"}`), "/tmp"); got != "" {
		t.Errorf("Read should not match Bash rule, got %q", got)
	}
}

func TestEvaluate_StrReplace(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["StrReplace(./**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	cwd := "/projects/myapp"
	if got := se.Evaluate("StrReplace", json.RawMessage(`{"file_path":"/projects/myapp/src/main.go"}`), cwd); got != "allow" {
		t.Errorf("StrReplace = %q, want allow", got)
	}
}

func TestEvaluate_WriteTmp(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
		"permissions": {
			"allow": ["Write(/tmp/**)"]
		}
	}`)
	se, _ := NewSettingsEvaluator(path)

	if got := se.Evaluate("Write", json.RawMessage(`{"file_path":"/tmp/test.txt"}`), "/projects"); got != "allow" {
		t.Errorf("Write /tmp = %q, want allow", got)
	}
}
