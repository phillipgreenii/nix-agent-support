package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPluginHooksJSON_hasWrapperAndAllEvents(t *testing.T) {
	b, err := os.ReadFile("../../ccpool-plugin/hooks/hooks.json")
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse: %v", err)
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks.json MUST wrap events under a top-level \"hooks\" key: a bare " +
			"event map loads the plugin but SILENTLY never fires the hooks, so the " +
			"store goes stale and every waiter hangs to timeout")
	}
	for _, ev := range []string{"SessionStart", "Stop", "StopFailure", "Notification", "PreToolUse"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("missing hook event %q", ev)
		}
	}
	// PreToolUse must match AskUserQuestion and run `ccpool hook ask` (pg2-7a5b):
	// the deterministic needs_input detection hinges on this exact wiring.
	if !pluginHookHas(hooks, "PreToolUse", "AskUserQuestion", "ccpool hook ask") {
		t.Error("PreToolUse must match \"AskUserQuestion\" and run \"ccpool hook ask\"")
	}
}

// pluginHookHas reports whether event `ev` has an entry whose matcher == matcher
// and whose hooks include a command-type hook running `cmd` (best-effort over the
// loosely-typed parsed JSON).
func pluginHookHas(hooks map[string]any, ev, matcher, cmd string) bool {
	entries, ok := hooks[ev].([]any)
	if !ok {
		return false
	}
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok || m["matcher"] != matcher {
			continue
		}
		hs, ok := m["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if ok && hm["type"] == "command" && hm["command"] == cmd {
				return true
			}
		}
	}
	return false
}
