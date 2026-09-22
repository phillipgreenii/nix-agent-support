package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureSlog temporarily replaces slog's default logger with one that
// records every record's message into a slice, runs fn, restores the prior
// default, and returns the captured messages in emission order. Used by this
// file's own tests to observe reconcileClosedBeadSessions's log lines
// without depending on os.Stderr pipe timing (poststartup_test.go's
// captureStdout/redirectStdin cover stdin/stdout; slog needs its own seam
// since nothing in this module redirects its default handler already).
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

// TestRunDispatch_reconciliationRunsForCcpoolRole proves pg2-hrppg's fix is
// actually wired into the dispatch path production uses (per the
// coordinator's own verification that query.go's hook never fires there):
// dispatching a ccpool-type role reaches reconcileClosedBeadSessions, which
// attempts a real `ccpool list` — observable here because PATH is broken
// (mirrors TestRunDispatch_poolFullExitsBusyNoBody's own isolation trick),
// so that attempt fails soft with reconcile.go's own "reconcile: list
// failed" log line rather than ever touching a real ccpool pool.
func TestRunDispatch_reconciliationRunsForCcpoolRole(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	dir := t.TempDir()
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"worker","type":"ccpool","ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello"}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatalf("write role config: %v", err)
	}

	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()

	var msgs []string
	_ = captureStdout(t, func() {
		msgs = captureSlog(t, func() {
			runDispatch([]string{"--role-config", rolePath})
		})
	})
	if !containsSubstring(msgs, "reconcile: list failed") {
		t.Fatalf("runDispatch for a ccpool role must reach reconcileClosedBeadSessions (observed via its list-failure log); got messages=%v", msgs)
	}
}

// TestRunDispatch_reconciliationSkippedForCommandRole proves a "command"
// role dispatch never reaches reconcileClosedBeadSessions at all — it has no
// ccpool sessions to reconcile, and (per dispatch.go's own doc comment on
// the role.CCPool != nil guard) must not touch ccpool/bd on its own account,
// exactly as it did not before this fix.
func TestRunDispatch_reconciliationSkippedForCommandRole(t *testing.T) {
	dir := t.TempDir()
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"conformance-command","type":"command","command":{"argv":["true"]}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatalf("write role config: %v", err)
	}

	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()

	var msgs []string
	_ = captureStdout(t, func() {
		msgs = captureSlog(t, func() {
			runDispatch([]string{"--role-config", rolePath})
		})
	})
	for _, m := range msgs {
		if strings.Contains(m, "reconcile:") {
			t.Fatalf("a command-role dispatch must never reach reconcileClosedBeadSessions; got messages=%v", msgs)
		}
	}
}
