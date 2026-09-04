package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeBackend creates an executable shell script named name on a
// temp PATH directory (prepended to $PATH for the duration of the test)
// that discards stdin and prints stdout verbatim, regardless of the op it
// was called with. Used to exercise Dispatch/FanOutAuthStatus/
// FanOutConfigValidate against a real exec'd process without needing a
// full pg-connector-*-github style binary.
func writeFakeBackend(t *testing.T, name, stdout string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	content := "#!/bin/sh\ncat >/dev/null\ncat <<'FAKE_BACKEND_EOF'\n" + stdout + "\nFAKE_BACKEND_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeOpAwareFakeBackend is like writeFakeBackend, but replies with a
// different canned response depending on which op the incoming request
// names — needed to fake a backend that answers auth_status and
// capabilities differently in the same test.
func writeOpAwareFakeBackend(t *testing.T, name string, byOp map[string]string, defaultStdout string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("req=$(cat)\n")
	for op, resp := range byOp {
		fmt.Fprintf(&sb, "if echo \"$req\" | grep -q '\"op\":\"%s\"'; then cat <<'FAKE_BACKEND_EOF'\n%s\nFAKE_BACKEND_EOF\nexit 0\nfi\n", op, resp)
	}
	fmt.Fprintf(&sb, "cat <<'FAKE_BACKEND_EOF'\n%s\nFAKE_BACKEND_EOF\n", defaultStdout)

	if err := os.WriteFile(script, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
