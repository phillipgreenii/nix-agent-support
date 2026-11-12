package originguard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func stub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pg-origin-guard")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecCheck_exitCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		avail   bool
		reason  string
		wantErr bool
	}{
		"available":   {"echo 'available realm=zr reason=ok'; exit 0", true, "ok", false},
		"unavailable": {"echo 'unavailable realm=zr reason=cert-expired'; exit 3", false, "cert-expired", false},
		"fault":       {"exit 1", false, "", true},
		"usage":       {"exit 2", false, "", true},
		"config":      {"exit 7", false, "", true},
		"old-75":      {"exit 75", false, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := ExecCheck(stub(t, tc.body))(context.Background(), "/repo")
			if (err != nil) != tc.wantErr || v.Available != tc.avail || v.Reason != tc.reason {
				t.Fatalf("got %+v, %v", v, err)
			}
		})
	}
}

// TestExitCodesPinPgOriginGuardContract pins the codes this client branches on
// to pg-origin-guard's published contract (its README exit-code table): 0
// available, 3 unavailable. A drift in the guard's codes must fail here rather
// than silently turn every unavailable answer into a fail-open fault.
func TestExitCodesPinPgOriginGuardContract(t *testing.T) {
	if exitAvailable != 0 || exitUnavailable != 3 {
		t.Fatalf("available=%d unavailable=%d, want 0 and 3", exitAvailable, exitUnavailable)
	}
}

func TestExecCheck_passesRepoArgs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	bin := stub(t, `echo "$@" > `+log+"\nexit 0")
	if _, err := ExecCheck(bin)(context.Background(), "/some/repo"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "check --repo /some/repo\n" {
		t.Fatalf("argv %q", b)
	}
}

func TestExecCheck_missingBinaryIsFault(t *testing.T) {
	if _, err := ExecCheck("/nonexistent/pg-origin-guard")(context.Background(), "/r"); err == nil {
		t.Fatal("want fault")
	}
}
