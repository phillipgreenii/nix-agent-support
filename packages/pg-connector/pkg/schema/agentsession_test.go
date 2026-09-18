package schema

import (
	"encoding/json"
	"testing"
)

func TestAgentSession_JSONFieldNames(t *testing.T) {
	pid := 4567
	s := AgentSession{
		SessionID: "s1", PID: &pid, Cwd: "/repo", Model: "claude-sonnet-5",
		Status: "blocked", Blocker: "usage_limit", Tokens: 12345, CostUSD: 1.23,
		AsOf: "2026-09-18T12:00:00Z", Stale: false,
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, key := range []string{"session_id", "pid", "cwd", "model", "status", "blocker", "tokens", "cost_usd", "as_of"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing expected JSON key %q in %s", key, raw)
		}
	}
	if _, ok := m["transcript_path"]; ok {
		t.Errorf("AgentSession must not carry a transcript path field, got %s", raw)
	}
}

func TestAgentSession_DeadPIDOmitted(t *testing.T) {
	raw, _ := json.Marshal(AgentSession{SessionID: "s1"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["pid"]; ok {
		t.Errorf("pid should be omitted when nil, got %s", raw)
	}
}
