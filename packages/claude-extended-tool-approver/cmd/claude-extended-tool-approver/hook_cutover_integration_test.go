//go:build integration

// Live trigger of the wired hook set for the per-machine cut-over switch: a real
// PermissionRequest payload goes to the compiled binary's stdin, with its database
// under a throwaway directory. Switch off: a row is recorded. Switch on: none is.

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func permissionRequestRows(t *testing.T, switchValue string) int {
	t.Helper()
	p := bashPayload(secretCWD, "default", "some-unknown-tool --flag")
	p.HookEventName = "PermissionRequest"
	body, _ := json.Marshal(p)
	env := hermeticEnv(t, "CETA_TRACKER_CUTOVER="+switchValue)
	cmd := exec.Command(testBinary(t))
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}
	var data string
	for _, kv := range env {
		if len(kv) > 14 && kv[:14] == "XDG_DATA_HOME=" {
			data = kv[14:]
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_decisions`).Scan(&n); err != nil {
		return 0 // no database at all: nothing was recorded
	}
	return n
}

func TestCutoverLive_PermissionRequestRecordedOnlyWithSwitchOff(t *testing.T) {
	if n := permissionRequestRows(t, ""); n != 1 {
		t.Errorf("switch off: %d rows, want 1", n)
	}
	if n := permissionRequestRows(t, "1"); n != 0 {
		t.Errorf("switch on: %d rows, want 0", n)
	}
}
