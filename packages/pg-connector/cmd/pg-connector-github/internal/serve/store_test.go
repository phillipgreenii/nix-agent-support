package serve_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // writes a store from a newer binary

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Error table rows for the store: migration running, store corrupt, store newer
// than the binary. The store lives in the state directory as store.sqlite.

func TestErrorTable_MigrationRunning_IsUnavailableWithAMigratingMessageThenServes(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{
		Hooks:  "migration_delay=2s",
		NoWait: true,
		Before: seed,
	})
	d.Start(t)

	// The daemon accepts connections at once (an apply's health check connects)
	// and answers unavailable with a migrating: message while the store migrates.
	var resp scriptout.Response
	until(t, 15*time.Second, "the daemon to accept a connection and answer", func() bool {
		r, err := d.Call("pr", scriptout.Request{Op: scriptout.OpList, Args: []byte(`{"query":"mine"}`)}, 5*time.Second)
		if err != nil {
			return false
		}
		resp = r
		return true
	})
	if resp.Error == nil || resp.Error.Code != "unavailable" || !strings.HasPrefix(resp.Error.Message, "migrating:") {
		t.Fatalf("during the migration want unavailable with a migrating: message, got %+v", resp.Error)
	}
	d.WaitReady(t)
	mustList(t, d, servetest.QueryMine, nil)
	if !servetest.HasChange(d.DaemonLog(t), "store_migrated") {
		t.Error("the migration must be logged as store_migrated")
	}
}

func TestErrorTable_StoreCorrupt_IsMovedAsideAndTheDaemonStartsEmpty(t *testing.T) {
	garbage := []byte(strings.Repeat("this is not a sqlite database; ", 200))
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		seed(d)
		if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.StorePath(), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	d.Start(t)

	// The daemon runs, on an empty store, and refills (governed by its caps).
	res := mustList(t, d, servetest.QueryMine, nil)
	if len(res.Entities) != 3 {
		t.Errorf("the daemon must refill from the origin: %v", res.PresentIDs)
	}

	moved, err := filepath.Glob(d.StorePath() + ".moved-aside-*")
	if err != nil || len(moved) != 1 {
		t.Fatalf("want exactly one moved-aside store, got %v (%v)", moved, err)
	}
	if got, err := os.ReadFile(moved[0]); err != nil || !bytes.Equal(got, garbage) {
		t.Errorf("the moved-aside file must be the corrupt store, untouched (%v)", err)
	}
	var line map[string]any
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "store_moved_aside" {
			line = l
		}
	}
	if line == nil || line["level"] != "warn" || line["moved_to"] != moved[0] {
		t.Errorf("the move-aside must be logged (warn, naming the new path): %v", line)
	}
}

func TestErrorTable_StoreNewerThanTheBinary_RefusesToStartAndNeverMovesTheFile(t *testing.T) {
	var before [32]byte
	var beforeSize int64
	d := servetest.NewDaemon(t, servetest.Options{NoWait: true, Before: func(d *servetest.Daemon) {
		if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// A store a newer binary wrote: schema version 99.
		db, err := sql.Open("sqlite", "file:"+d.StorePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE from_the_future (x INTEGER); PRAGMA user_version = 99`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(d.StorePath())
		if err != nil {
			t.Fatal(err)
		}
		before, beforeSize = sha256.Sum256(raw), int64(len(raw))
	}})
	d.Start(t)

	if code := d.Wait(t, 20*time.Second); code != 5 {
		t.Fatalf("serve exit = %d, want 5; output: %s", code, d.Stderr())
	}
	if !strings.Contains(d.Stderr(), "schema version 99") || !strings.Contains(d.Stderr(), "downgrade") {
		t.Errorf("stderr must say why: %q", d.Stderr())
	}
	var line map[string]any
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "store_refused" {
			line = l
		}
	}
	if line == nil || line["level"] != "error" || line["found_version"] != float64(99) {
		t.Errorf("the refusal must be logged at error level with the found version: %v", line)
	}

	// The file is exactly as it was: not modified, not moved, no sidecars.
	raw, err := os.ReadFile(d.StorePath())
	if err != nil {
		t.Fatalf("the store file must still be in place: %v", err)
	}
	if sha256.Sum256(raw) != before || int64(len(raw)) != beforeSize {
		t.Error("the refused store was modified")
	}
	entries, err := os.ReadDir(d.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "store.sqlite") && e.Name() != "store.sqlite" {
			t.Errorf("unexpected file beside the refused store: %s", e.Name())
		}
	}
	// Nothing is left listening.
	if _, err := os.Lstat(d.Socket); !os.IsNotExist(err) {
		t.Errorf("the refusing daemon must remove its socket: %v", err)
	}
}
