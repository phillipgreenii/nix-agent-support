package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func runNamedCmd(t *testing.T, name string) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{name})
	if ferr != nil {
		t.Fatalf("rootCmd has no %s subcommand: %v", name, ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, nil)
	return buf.String(), err
}

func TestHeartbeatUpdatesLastHeartbeat(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	origNow := heartbeatNow
	t.Cleanup(func() { heartbeatNow = origNow })
	heartbeatNow = func() string { return "2026-09-16T12:00:00Z" }

	if _, err := runNamedCmd(t, "heartbeat"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got, found, err := st.GetMeta(store.MetaKeyLastHeartbeat)
	if err != nil || !found {
		t.Fatalf("GetMeta(last_heartbeat): found=%v err=%v", found, err)
	}
	if got != "2026-09-16T12:00:00Z" {
		t.Errorf("last_heartbeat = %q, want the stamped time", got)
	}
}

// TestHeartbeatItemTouchesNeitherStoreNorPipeline pins this packet's own
// Acceptance Criteria: "heartbeat-item prints its synthetic query item
// WITHOUT touching the store or invoking the pipeline." No store seam is
// stubbed at all here — if heartbeat-item ever called deskStoreOpen, this
// test would panic on the nil *store.Store the zero-value seam returns.
func TestHeartbeatItemTouchesNeitherStoreNorPipeline(t *testing.T) {
	origStoreOpen := deskStoreOpen
	t.Cleanup(func() { deskStoreOpen = origStoreOpen })
	storeOpenCalled := false
	deskStoreOpen = func() (*store.Store, error) {
		storeOpenCalled = true
		return nil, nil
	}

	origNow := heartbeatItemNow
	t.Cleanup(func() { heartbeatItemNow = origNow })
	heartbeatItemNow = func() string { return "2026-09-16T12:00:00Z" }

	stdout, err := runNamedCmd(t, "heartbeat-item")
	if err != nil {
		t.Fatalf("heartbeat-item: %v", err)
	}
	if storeOpenCalled {
		t.Fatal("heartbeat-item touched the store")
	}

	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("stdout is not a bare JSON array: %v\nstdout: %s", err, stdout)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0]["id"] != "2026-09-16T12:00:00Z" {
		t.Errorf(`item["id"] = %v, want the current RFC3339 timestamp`, items[0]["id"])
	}
	if _, present := items[0]["type"]; !present {
		t.Errorf("item missing \"type\": %+v", items[0])
	}
	if _, present := items[0]["metadata"]; !present {
		t.Errorf("item missing \"metadata\": %+v", items[0])
	}
}
