package main

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"
)

func containsSubstring(msgs []string, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// worktreeRoleCfg is a ccpool role config that leaves isolation at its default,
// "worktree" (extraCC is spliced at the front of the ccpool table).
func worktreeRoleCfg(extraCC string) map[string]any {
	return mustCfg(`{"ccpool":{` + extraCC + `"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello"}}`)
}

// sweepDispatch runs one dispatch of the named role against a launch config
// holding extraLaunch, with PATH broken so every ccpool call fails soft and is
// observable only through its log line, and returns the log messages.
func sweepDispatch(t *testing.T, cfg map[string]any, extraLaunch string) []string {
	t.Helper()
	t.Setenv("PATH", "/usr/bin")
	cfgPath, _ := dtest.LaunchConfig(t, extraLaunch)
	msgs := dtest.CaptureSlog(t, func() {
		dispatchWith(t, "worker", cfg, cfgPath, dtest.DefaultEvent)
	})
	return msgs
}
