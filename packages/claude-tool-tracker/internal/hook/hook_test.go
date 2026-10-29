package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/events"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseFixtures(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	cases := []struct {
		file, kind string
		toolUse    string
		check      func(t *testing.T, d map[string]any)
	}{
		{"pre_tool_use.json", "tracker.pre_tool", "toolu_1", func(t *testing.T, d map[string]any) {
			if d["permission_mode"] != "auto" || d["agent_type"] != "code-reviewer" || d["tool_name"] != "Bash" {
				t.Errorf("%v", d)
			}
		}},
		{"post_tool_use.json", "tracker.post_tool", "toolu_1", func(t *testing.T, d map[string]any) {
			if d["duration_ms"] != 42.0 || d["is_error"] != false || d["exit_code"] != 0.0 {
				t.Errorf("%v", d)
			}
			if _, raw := d["tool_response"]; raw {
				t.Error("raw payload must not be recorded")
			}
			if d["response_bytes"].(int) <= 0 {
				t.Errorf("%v", d)
			}
		}},
		{"post_tool_use_failure.json", "tracker.post_tool", "toolu_2", func(t *testing.T, d map[string]any) {
			if d["is_error"] != true || d["error"] != "boom" || d["is_interrupt"] != true {
				t.Errorf("%v", d)
			}
		}},
		{"permission_request.json", "tracker.permission_offer", "", func(t *testing.T, d map[string]any) {
			if d["suggestions"] == nil || d["tool_input"] == nil {
				t.Errorf("%v", d)
			}
		}},
		{"permission_denied.json", "tracker.denied", "toolu_3", func(t *testing.T, d map[string]any) {
			if d["reason"] != "classifier says no" {
				t.Errorf("%v", d)
			}
		}},
	}
	for _, c := range cases {
		env, ok := Parse(strings.NewReader(fixture(t, c.file)), now)
		if !ok {
			t.Fatalf("%s: not parsed", c.file)
		}
		if env.Kind != c.kind || env.ToolUseID != c.toolUse || env.Source != "tracker" || env.SessionID != "sess-1" || env.TS != 1700000000000 {
			t.Errorf("%s: %+v", c.file, env)
		}
		if err := env.Validate(); err != nil {
			t.Errorf("%s: %v", c.file, err)
		}
		c.check(t, env.Data)
	}
}

func TestParseSubagentCarriesAgentID(t *testing.T) {
	env, ok := Parse(strings.NewReader(fixture(t, "pre_tool_use.json")), time.Now())
	if !ok || env.AgentID != "agent-9" {
		t.Fatalf("%+v", env)
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"blank":       " \n",
		"malformed":   "{not json",
		"no session":  `{"hook_event_name":"PreToolUse"}`,
		"other event": `{"hook_event_name":"Stop","session_id":"s"}`,
		"array":       `[1]`,
	} {
		if env, ok := Parse(strings.NewReader(in), time.Now()); ok {
			t.Errorf("%s: parsed %+v", name, env)
		}
	}
}

func TestRunWritesEvent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Run(strings.NewReader(fixture(t, "pre_tool_use.json")), time.Now())
	dir, _ := events.Dir()
	if _, err := os.Stat(filepath.Join(dir, events.FileName("sess-1", "tracker"))); err != nil {
		t.Fatal(err)
	}
}
