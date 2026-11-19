package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// captureSlog temporarily replaces slog's default logger with one that
// records every record's message into a slice, runs fn, restores the prior
// default, and returns the captured messages in emission order. Used to observe
// the sweep passes' log lines without depending on os.Stderr pipe timing.
func captureSlog(t *testing.T, fn func()) []string {
	t.Helper()
	rec := &recordingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(old)
	fn()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.messages))
	copy(out, rec.messages)
	return out
}

type recordingHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

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
	cfgPath, _ := launchConfig(t, extraLaunch)
	msgs := captureSlog(t, func() {
		dispatchWith(t, "worker", cfg, cfgPath, defaultEvent)
	})
	return msgs
}
