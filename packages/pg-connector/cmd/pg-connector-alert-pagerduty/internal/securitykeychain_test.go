package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// securityStub puts an executable named `security` on PATH running script.
func securityStub(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSecurityKeychain_ReadsPasswordFromRealProcess runs the production
// keychain reader against a real stub process: stdout is the trimmed password
// and the argv names the item.
func TestSecurityKeychain_ReadsPasswordFromRealProcess(t *testing.T) {
	securityStub(t, `echo "$*" >&2; echo "  s3cret  "`)
	got, err := securityKeychain{}.Password(context.Background(), "svc", "acct")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("password = %q, want s3cret", got)
	}
}

// TestSecurityKeychain_NonZeroExitIsAnError: a missing item (security exits
// non-zero) is an error naming the item, so the resolver falls back to the
// environment.
func TestSecurityKeychain_NonZeroExitIsAnError(t *testing.T) {
	securityStub(t, `exit 44`)
	_, err := securityKeychain{}.Password(context.Background(), "svc", "acct")
	if err == nil || !strings.Contains(err.Error(), "svc/acct") || !strings.Contains(err.Error(), "exit status 44") {
		t.Fatalf("err = %v, want an exit-status error naming svc/acct", err)
	}
}

// TestSecurityKeychain_CancelledContextEndsTheProcess: a cancelled context
// ends a hung security within the grace instead of hanging the resolver.
func TestSecurityKeychain_CancelledContextEndsTheProcess(t *testing.T) {
	securityStub(t, `sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (securityKeychain{}).Password(ctx, "svc", "acct"); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}
