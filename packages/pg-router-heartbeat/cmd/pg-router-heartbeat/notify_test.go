package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notify.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// runPaging runs run() with silence already past the threshold, so the tick
// pages through the stub.
func runPaging(t *testing.T, stub string) (code int, errOut string, st state, elapsed time.Duration) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	dir := t.TempDir()
	seed, _ := json.Marshal(state{LastRun: time.Now().Add(-time.Minute), Silence: 40 * time.Minute})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), seed, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	start := time.Now()
	code = run([]string{
		"--prometheus-url", srv.URL, "--state-dir", dir,
		"--notify-command", stub + " {message}",
		"--notify-timeout", "300ms", "--notify-wait-delay", "300ms",
	}, &out, &errb)
	elapsed = time.Since(start)
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return code, errb.String(), st, elapsed
}

func TestHungNotifierIsBoundedAndRetried(t *testing.T) {
	code, errOut, st, elapsed := runPaging(t, writeStub(t, "exec sleep 600"))
	if elapsed > 3*time.Second {
		t.Fatalf("run took %v, want about timeout plus WaitDelay", elapsed)
	}
	if code != 1 || !strings.Contains(errOut, "timed out") {
		t.Fatalf("code=%d stderr=%q, want 1 with the timeout error", code, errOut)
	}
	if !st.PagedAt.IsZero() {
		t.Fatalf("PagedAt = %v, want unset so the page retries next tick", st.PagedAt)
	}
	if st.LastRun.IsZero() {
		t.Fatal("state was not saved")
	}
}

func TestPromptNotifierPages(t *testing.T) {
	code, errOut, st, _ := runPaging(t, writeStub(t, "exit 0"))
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q, want clean exit", code, errOut)
	}
	if st.PagedAt.IsZero() {
		t.Fatal("PagedAt unset after a successful page")
	}
}
