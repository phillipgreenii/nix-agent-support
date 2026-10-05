package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const queuesPath = "../../../../claude-marketplace/pb/queues.json"

func writeConfig(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(queuesPath)
	if err != nil {
		t.Fatal(err)
	}
	var queues []any
	if err := json.Unmarshal(raw, &queues); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"bdPath":                  "/nonexistent/bd",
		"childPath":               "/nonexistent/bash/bin:/nonexistent/coreutils/bin",
		"beadsDirs":               map[string]string{"alpha": "/nonexistent/alpha/.beads"},
		"claudeDir":               "/nonexistent/claude",
		"operatorNames":           []string{"operator"},
		"port":                    9100,
		"pollIntervalSeconds":     60,
		"strandedIntervalSeconds": 300,
		"staleClaimHours":         4,
		"commandTimeoutSeconds":   30,
		"labelCap":                20,
		"queues":                  queues,
	}
	if mutate != nil {
		mutate(m)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheckConfigAcceptsAValidFileWithoutTouchingTheFilesystem(t *testing.T) {
	// Every path in the file is nonexistent: the check must be schema-only.
	code, out, errs := runCmd("-check-config", writeConfig(t, nil))
	if code != exitOK || strings.TrimSpace(out) != "ok" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestCheckConfigRejectsBadFiles(t *testing.T) {
	code, _, errs := runCmd("-check-config", writeConfig(t, func(m map[string]any) { m["port"] = 0 }))
	if code != exitConfig || !strings.Contains(errs, "port") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	code, _, errs = runCmd("-check-config", writeConfig(t, func(m map[string]any) {
		m["queues"] = []map[string]any{{"name": "bad", "args": []string{"--claim"}}}
	}))
	if code != exitConfig || !strings.Contains(errs, "--claim") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	code, _, _ = runCmd("-check-config", filepath.Join(t.TempDir(), "missing.json"))
	if code != exitConfig {
		t.Fatalf("missing file code = %d", code)
	}
}

func TestVersionAndUsage(t *testing.T) {
	old := Version
	Version = "0.0.0-abcdef01"
	defer func() { Version = old }()
	code, out, _ := runCmd("--version")
	if code != exitOK || strings.TrimSpace(out) != "0.0.0-abcdef01" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, _, errs := runCmd()
	if code != exitUsage || !strings.Contains(errs, "usage") {
		t.Fatalf("no-args code=%d err=%q", code, errs)
	}
	code, _, _ = runCmd("-bogus")
	if code != exitUsage {
		t.Fatalf("bad flag code = %d", code)
	}
}

func TestRunWithABadConfigExitsWithConfigCode(t *testing.T) {
	code, _, errs := runCmd("-config", writeConfig(t, func(m map[string]any) { m["labelCap"] = 0 }))
	if code != exitConfig || !strings.Contains(errs, "labelCap") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestExitCodesKeepOneForUnexpectedErrors(t *testing.T) {
	if exitInternal != 1 || exitUsage < 2 || exitConfig < 2 || exitConfig == exitUsage {
		t.Fatal("exit codes: 1 is the generic error; specific meanings must be >= 2 and distinct")
	}
}
