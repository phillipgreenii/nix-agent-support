package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckGrafanaAlertsEmpty(t *testing.T) {
	findings := checkGrafanaAlerts(nil)
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %d", len(findings))
	}
}

func TestCheckGrafanaAlertsOnePerAlert(t *testing.T) {
	alerts := []grafanaAlert{
		{RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active", StartsAt: time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC), Values: "B=3"},
		{RuleUID: "pg-router-failure-rate", Labels: map[string]string{"instance": "b"}, State: "active"},
	}
	findings := checkGrafanaAlerts(alerts)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Kind != kindGrafanaAlert {
		t.Fatalf("got kind %q", findings[0].Kind)
	}
	if findings[0].Fingerprint != "pg-router-liveness-down|instance=a" {
		t.Fatalf("got fingerprint %q", findings[0].Fingerprint)
	}
	if !findings[0].StartsAt.Equal(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)) || findings[0].Values != "B=3" {
		t.Fatalf("got StartsAt=%v Values=%q", findings[0].StartsAt, findings[0].Values)
	}
	if !strings.Contains(findings[0].Evidence, "starts_at=2026-10-05T01:02:03Z") {
		t.Fatalf("evidence must print starts_at, got %q", findings[0].Evidence)
	}
	if len(findings[0].Aliases) != 1 || findings[0].Aliases[0] != findings[0].Fingerprint {
		t.Fatalf("expected aliases to include own fingerprint, got %v", findings[0].Aliases)
	}
}

func TestCheckBinaryHash(t *testing.T) {
	const (
		storeA = "/nix/store/aaaa-pg-router-ccpool-handler-1/bin/pg-router-ccpool-handler"
		storeB = "/nix/store/bbbb-pg-router-ccpool-handler-2/bin/pg-router-ccpool-handler"
		plain  = "/opt/handler/bin/handler"
	)
	cases := []struct {
		name           string
		hasPrevious    bool
		prev, cur      binaryIdentity
		deployExpected bool
		wantFinding    bool
	}{
		{"no prior baseline", false, binaryIdentity{}, binaryIdentity{"h2", storeA}, false, false},
		{"prior baseline with empty hash", true, binaryIdentity{"", storeA}, binaryIdentity{"h2", storeA}, false, false},
		{"same path same hash", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h1", storeA}, false, false},
		{"different store path different hash is an expected update", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h2", storeB}, false, false},
		{"different store path same hash", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h1", storeB}, false, false},
		{"same store object different hash is tampering", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h2", storeA}, false, true},
		{"same store object different file different hash is tampering", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h2", "/nix/store/aaaa-pg-router-ccpool-handler-1/libexec/x"}, false, true},
		{"same store path different hash but deploy record allows", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h2", storeA}, true, false},
		{"non-store path different hash alerts", true, binaryIdentity{"h1", plain}, binaryIdentity{"h2", plain}, false, true},
		{"non-store path different hash allowed by deploy record", true, binaryIdentity{"h1", plain}, binaryIdentity{"h2", plain}, true, false},
		{"moved to store path from non-store path is an expected update", true, binaryIdentity{"h1", plain}, binaryIdentity{"h2", storeA}, false, false},
		{"moved from store path to non-store path alerts", true, binaryIdentity{"h1", storeA}, binaryIdentity{"h2", plain}, false, true},
		{"old snapshot without path, store path now: expected update", true, binaryIdentity{"h1", ""}, binaryIdentity{"h2", storeA}, false, false},
		{"old snapshot without path, non-store path now: falls back to alert", true, binaryIdentity{"h1", ""}, binaryIdentity{"h2", plain}, false, true},
		{"old snapshot without path, non-store path, deploy record allows", true, binaryIdentity{"h1", ""}, binaryIdentity{"h2", plain}, true, false},
		{"old snapshot without path, same hash", true, binaryIdentity{"h1", ""}, binaryIdentity{"h1", storeA}, false, false},
		{"bare store root is not a store object", true, binaryIdentity{"h1", "/nix/store/"}, binaryIdentity{"h2", "/nix/store/"}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := checkBinaryHash(c.hasPrevious, c.prev, c.cur, c.deployExpected)
			if (f != nil) != c.wantFinding {
				t.Fatalf("finding = %+v, wantFinding %v", f, c.wantFinding)
			}
			if f == nil {
				return
			}
			if f.Kind != kindBinaryHashMismatch {
				t.Fatalf("got kind %q", f.Kind)
			}
			if f.Fingerprint != binaryHashMismatchFingerprint {
				t.Fatalf("got fingerprint %q", f.Fingerprint)
			}
			if f.State != c.cur.Hash {
				t.Fatalf("got state %q, want %q", f.State, c.cur.Hash)
			}
			if !strings.Contains(f.Evidence, "current_path="+c.cur.Path) {
				t.Fatalf("evidence lacks the current path: %q", f.Evidence)
			}
		})
	}
}

func TestStoreObject(t *testing.T) {
	cases := map[string]string{
		"/nix/store/abc-name/bin/x": "/nix/store/abc-name",
		"/nix/store/abc-name":       "/nix/store/abc-name",
		"/nix/store/":               "",
		"/nix/storex/abc/bin":       "",
		"/opt/x":                    "",
		"":                          "",
	}
	for in, want := range cases {
		if got := storeObject(in); got != want {
			t.Errorf("storeObject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(path, []byte("hello world"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	// sha256("hello world") -- verified via `printf 'hello world' | shasum -a 256`
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHashFileMissing(t *testing.T) {
	_, err := hashFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected an error for a missing file")
	}
}
