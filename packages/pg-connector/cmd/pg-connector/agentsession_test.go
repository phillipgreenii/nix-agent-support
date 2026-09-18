package main

import "testing"

func TestNewAgentSessionCmd_HasShowAndListOnly(t *testing.T) {
	cmd := newAgentSessionCmd()
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	if !names["show"] || !names["list"] {
		t.Fatalf("expected show and list subcommands, got %v", names)
	}
	for _, forbidden := range []string{"create", "update", "close"} {
		if names[forbidden] {
			t.Errorf("agentsession is read-only; unexpected %q subcommand", forbidden)
		}
	}
}

func TestHumanizeAgentSession_RoundTrip(t *testing.T) {
	raw := []byte(`{"session_id":"s1","status":"blocked","blocker":"usage_limit","model":"claude-sonnet-5","cwd":"/repo","tokens":100,"cost_usd":0.5}`)
	out, err := humanizeAgentSession(raw)
	if err != nil {
		t.Fatalf("humanizeAgentSession: %v", err)
	}
	if !contains(out, "s1") || !contains(out, "blocked/usage_limit") {
		t.Errorf("got %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
