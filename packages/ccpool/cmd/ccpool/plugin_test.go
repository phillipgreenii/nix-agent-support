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
		t.Fatal("hooks.json MUST wrap events under a top-level \"hooks\" key (spec §9)")
	}
	for _, ev := range []string{"SessionStart", "Stop", "StopFailure", "Notification"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("missing hook event %q", ev)
		}
	}
}
