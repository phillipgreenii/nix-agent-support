package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// bootCore compacts queue.jsonl at startup (bead pg2-8e0m6): a log bloated with
// evicted events shrinks to its live state before the queue accepts anything, the
// live event and the active gate survive, and the size the queue reports (the
// gauge / status source) is the compacted file's.
func TestBootCore_CompactsQueueLogAtStartup(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "queue.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	at := time.Now().UTC()
	exp := at.Add(time.Hour)
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("dead-%d", i)
		for _, rec := range []map[string]any{
			{"op": "enqueue", "eventId": id, "type": "t1", "at": at, "expiresAt": at, "enqueuedAt": at},
			{"op": "accept", "eventId": id, "listenerId": "r1"},
			{"op": "evict", "eventId": id},
		} {
			if err := enc.Encode(rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, rec := range []map[string]any{
		{"op": "enqueue", "eventId": "live-1", "type": "t1", "at": at, "expiresAt": exp, "enqueuedAt": at},
		{"op": "gate_set", "eventId": "", "gateType": "SYSTEM_PAUSE", "at": at, "owner": "operator"},
	} {
		if err := enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	before, _ := os.Stat(path)

	cfg := config.Config{
		LogDir:                dir,
		CompactThresholdBytes: 0, // runtime compaction off; startup compaction is unconditional
		Roles:                 roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, q, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	after, _ := os.Stat(path)
	if after.Size()*10 > before.Size() {
		t.Fatalf("queue.jsonl not compacted at startup: %d -> %d bytes", before.Size(), after.Size())
	}
	if q.LogSize() != after.Size() {
		t.Fatalf("q.LogSize() = %d, file is %d bytes", q.LogSize(), after.Size())
	}
	if q.Compactions() != 1 {
		t.Fatalf("Compactions = %d, want 1", q.Compactions())
	}
	if got := q.DepthByType()["t1"]; got != 1 {
		t.Fatalf("live event lost across startup compaction: depth = %v", q.DepthByType())
	}
	if gates := q.ActiveGates(); len(gates) != 1 || gates[0].Type != "SYSTEM_PAUSE" {
		t.Fatalf("active gate lost across startup compaction: %v", gates)
	}
}

// A second bootCore over the same LogDir (a double start, a run-until-idle beside
// the daemon) is refused before it can compact or rename the log under the first
// one (bead pg2-maxn1): the error names the lock and the log is untouched.
func TestBootCore_RefusesSecondOpenerOfTheLog(t *testing.T) {
	dir := shortDir(t)
	cfg := config.Config{
		LogDir: dir,
		Roles:  roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
	}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, &orchestrator.Orchestrator{Cfg: cfg}, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("first bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()
	before, _ := os.ReadFile(filepath.Join(dir, "queue.jsonl"))

	_, _, _, _, err = bootCore(context.Background(), cfg, &orchestrator.Orchestrator{Cfg: cfg}, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	var locked *eventqueue.ErrLogLocked
	if !errors.As(err, &locked) {
		t.Fatalf("second bootCore err = %v, want *eventqueue.ErrLogLocked", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "queue.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("a refused second opener changed queue.jsonl")
	}
}
