package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The store outlives the process: a restart serves what was fresh before it, a
// store that cannot be used is moved aside and the daemon starts empty, a store
// newer than the binary is refused, and the logs rotate at their sizes.

// TestRestartServesTheFreshGroupsWithZeroOriginCalls: a restarted daemon answers
// a query and the issues of it from what the store holds fresh, without asking
// the origin once (the fake pjira's call log stays empty).
func TestRestartServesTheFreshGroupsWithZeroOriginCalls(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)
	// Warm every group: both summaries and details of the mine query's issues.
	invoke(t, b, `{"op":"show","args":{"id":"EXA-1"}}`).wantResult(t, "show EXA-1", schema.IssueSchemaVersion)
	invoke(t, b, `{"op":"show","args":{"id":"EXA-2"}}`).wantResult(t, "show EXA-2", schema.IssueSchemaVersion)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	d.Shim.ClearCalls()
	d.Start(t)
	list := invoke(t, b, string(listMine))
	list.wantResult(t, "list after the restart", schema.IssueSchemaVersion)
	var res schema.IssueListResult
	list.decode(t, "list", &res)
	if len(res.PresentIDs) != 2 || len(res.Entities) != 2 {
		t.Errorf("list after the restart = %+v, want both members from the store", res)
	}
	for _, id := range []string{"EXA-1", "EXA-2"} {
		invoke(t, b, `{"op":"show","args":{"id":"`+id+`"}}`).wantResult(t, "show "+id+" after the restart", schema.IssueSchemaVersion)
	}
	invoke(t, b, `{"op":"explain","args":{"id":"EXA-1"}}`).wantResult(t, "explain after the restart", schema.IssueSchemaVersion)

	// Give the scheduler the time a due task would take to run, with a poll
	// interval of 50 ms it would long have.
	time.Sleep(2 * time.Second)
	if calls := d.Shim.Calls(); len(calls) != 0 {
		t.Errorf("the restarted daemon made %d origin calls, want none: %v", len(calls), calls)
	}
}

var listMine = []byte(`{"op":"list","args":{"query":"` + servetest.QueryMine + `","cursor":null,"ids_only":false}}`)

// TestRestartAfterAStoreMoveAsideStartsEmptyAndEveryConsumerMustReset: a store
// that is corrupt is renamed aside with a timestamp and the daemon starts on a
// fresh one: the watched flags and the baseline markers are gone, and the first
// poll of every consumer, one that polled before and one that never did, answers
// invalid_argument cursor_expired:.
func TestRestartAfterAStoreMoveAsideStartsEmptyAndEveryConsumerMustReset(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	invoke(t, b, confChangesReq("before-move", servetest.QueryMine, nil)).wantResult(t, "tail poll", schema.IssueSchemaVersion)

	var watched int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM entity WHERE watched_at IS NOT NULL`).Scan(&watched); err != nil || watched == 0 {
		t.Fatalf("before the corruption %d entities are watched (%v), want the members of the watched query", watched, err)
	}
	before := storeIdentity(t, d)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// Corrupt the store: the header of an SQLite file is no longer one. The
	// origin cannot be asked while it is empty, so the flags a query run would
	// restore are observable.
	if err := os.WriteFile(d.StorePath(), bytes.Repeat([]byte("this is not a database\n"), 400), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		_ = os.Remove(d.StorePath() + sfx)
	}
	d.Shim.Hold("search")
	t.Cleanup(d.Shim.Release)
	d.Start(t)

	aside, _ := filepath.Glob(d.StorePath() + ".moved-aside-*")
	var kept []string
	for _, f := range aside {
		if !strings.HasSuffix(f, "-wal") && !strings.HasSuffix(f, "-shm") && !strings.HasSuffix(f, "-journal") {
			kept = append(kept, f)
		}
	}
	if len(kept) != 1 {
		t.Fatalf("moved-aside stores = %v, want exactly the one corrupt file renamed with a timestamp", aside)
	}
	if !servetest.HasChange(d.DaemonLog(t), "store_moved_aside") {
		t.Errorf("the daemon log has no store_moved_aside line: %v", d.DaemonLog(t))
	}
	db := d.OpenStore(t)
	var flags, baselines int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entity WHERE watched_at IS NOT NULL`).Scan(&flags); err != nil || flags != 0 {
		t.Errorf("after the move-aside %d entities are watched (%v), want the flags lost", flags, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM query WHERE baseline_at IS NOT NULL`).Scan(&baselines); err != nil || baselines != 0 {
		t.Errorf("after the move-aside %d baseline markers remain (%v), want them lost", baselines, err)
	}
	if after := storeIdentity(t, d); after == "" || after == before {
		t.Errorf("store identity %q after the move-aside, was %q, want a new one", after, before)
	}

	for _, consumer := range []string{"before-move", "never-polled"} {
		r := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil))
		r.wantError(t, "the first poll of "+consumer, "invalid_argument")
		if r.resp.Error != nil && !strings.Contains(r.resp.Error.Message, "cursor_expired:") {
			t.Errorf("%s: message %q must carry cursor_expired:", consumer, r.resp.Error.Message)
		}
	}
	// The consumer recovers with a reset, as the message says; the origin may
	// answer again, so the first run of the query can finish.
	d.Shim.Release()
	invoke(t, b, confChangesReq("before-move", servetest.QueryMine, map[string]any{"reset": true})).wantResult(t, "the reset poll", schema.IssueSchemaVersion)
}

// storeIdentity is the identity recorded in the store file as it is now.
func storeIdentity(t *testing.T, d *servetest.Daemon) string {
	t.Helper()
	var id string
	if err := d.OpenStore(t).QueryRow(`SELECT value FROM store_meta WHERE key = 'store_id'`).Scan(&id); err != nil {
		t.Fatalf("read the store identity: %v", err)
	}
	return id
}

// TestRestartRefusesAStoreNewerThanTheBinaryAndNeverTouchesIt: a downgrade exits
// 5 with a log line and leaves the file as it was: no move-aside, no write.
func TestRestartRefusesAStoreNewerThanTheBinaryAndNeverTouchesIt(t *testing.T) {
	d := confDaemon(t)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}
	w := d.WritableStore(t)
	if _, err := w.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	digest := func() [32]byte {
		raw, err := os.ReadFile(d.StorePath())
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(raw)
	}
	was := digest()

	d.StartNoWait(t)
	if code := d.Wait(t, 30*time.Second); code != 5 {
		t.Errorf("exit = %d, want 5 (refused); output: %s", code, d.Stderr())
	}
	if out := d.Stderr(); !strings.Contains(out, "9999") || !strings.Contains(out, "refusing to open") || !strings.Contains(out, "untouched") {
		t.Errorf("the refusal must say the store is newer than the binary and left untouched: %q", out)
	}
	if digest() != was {
		t.Error("a refused store was modified")
	}
	if aside, _ := filepath.Glob(d.StorePath() + ".moved-aside-*"); len(aside) != 0 {
		t.Errorf("a newer store was moved aside: %v", aside)
	}
}

// TestRestartMigratingWindowAnswersUnavailableMigrating: while the store
// migrates the daemon already listens, and every op answers unavailable with a
// message that starts migrating:, through the real client as well.
func TestRestartMigratingWindowAnswersUnavailableMigrating(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Hooks: "migration_delay=1500ms", NoWait: true, ClientBudget: "3s"})
	d.Start(t)
	var migrating bool
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !migrating {
		resp, err := d.Call(servetest.Op("show", `{"id":"EXA-1"}`), 5*time.Second)
		if err == nil && resp.Error != nil && strings.Contains(resp.Error.Message, "migrating:") {
			migrating = true
			if resp.Error.Code != "unavailable" {
				t.Errorf("the migrating answer is %q, want unavailable", resp.Error.Code)
			}
			// The same through the real client.
			res := d.Client(t, `{"op":"show","args":{"id":"EXA-1"}}`)
			if res.Exit != 4 || res.Resp.Error == nil || !strings.Contains(res.Resp.Error.Message, "migrating:") {
				t.Errorf("the client's answer while migrating: exit %d %s", res.Exit, res.Stdout)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !migrating {
		t.Fatalf("the daemon never answered migrating:; output: %s", d.Stderr())
	}
	d.WaitReady(t)
}

// TestLogsRotateAtTheirSizes: a full events log is renamed to events.jsonl.1 by
// the daemon, and a full client log by the clients, once however many clients
// race, with no row lost.
func TestLogsRotateAtTheirSizes(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{ClientBudget: "2s", Before: func(d *servetest.Daemon) {
		if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Past the daemon's 5 MiB and the client's 1 MiB.
		line := []byte(`{"time":"2026-10-01T00:00:00.000Z","level":"info","msg":"old row","service":"old"}` + "\n")
		fill := func(path string, size int) {
			if err := os.WriteFile(path, bytes.Repeat(line, size/len(line)+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fill(filepath.Join(d.StateDir, "events.jsonl"), 5<<20+1024)
		fill(filepath.Join(d.StateDir, "client.log.jsonl"), 1<<20+1024)
	}})

	// Eight clients at once find the daemon absent: each appends one row, and the
	// full log rotates exactly once.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Client(t, `{"op":"show","args":{"id":"EXA-1"}}`)
		}()
	}
	wg.Wait()
	rows := d.ClientLog(t)
	if len(rows) != 8 {
		t.Errorf("the live client log has %d rows, want the 8 of this run (the old rows rotated away)", len(rows))
	}
	for _, r := range rows {
		if r["outcome"] != "daemon_unreachable" {
			t.Errorf("client log row %v, want daemon_unreachable", r)
		}
	}
	if fi, err := os.Stat(filepath.Join(d.StateDir, "client.log.jsonl.1")); err != nil || fi.Size() < 1<<20 {
		t.Errorf("the rotated client log: %v %v, want the old rows", fi, err)
	}

	// The daemon rotates its own on its first rows.
	d.Start(t)
	until(t, 20*time.Second, "the event log to rotate", func() bool {
		_, err := os.Stat(filepath.Join(d.StateDir, "events.jsonl.1"))
		return err == nil
	})
	live := d.EventLog(t)
	if len(live) == 0 || len(live) > 100 {
		t.Errorf("the live event log has %d rows after the rotation, want only the new ones", len(live))
	}
	for _, r := range live {
		if r["service"] == "old" {
			t.Errorf("an old row stayed in the live event log: %v", r)
		}
	}
}

// TestRestartMigratesAnOlderStoreKeepingItsDataAndABackup: a store one rung below
// the binary's is backed up (VACUUM INTO, verified), migrated in place, and keeps
// what it held: the restarted daemon still answers from it with no origin call.
func TestRestartMigratesAnOlderStoreKeepingItsDataAndABackup(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)
	invoke(t, b, `{"op":"show","args":{"id":"EXA-1"}}`).wantResult(t, "show", schema.IssueSchemaVersion)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// Take the last rung back off: the entity's closed-at stamp, and the version
	// that says it was applied.
	w := d.WritableStore(t)
	for _, stmt := range []string{
		`DROP INDEX entity_closed_at`,
		`ALTER TABLE entity DROP COLUMN closed_at`,
		`PRAGMA user_version = 6`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	} {
		if _, err := w.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	d.Shim.ClearCalls()
	d.Start(t)
	if backups, _ := filepath.Glob(d.StorePath() + ".pre-schema-6-*"); len(backups) == 0 {
		t.Error("the migration of a populated store left no verified backup")
	}
	var version int
	if err := d.OpenStore(t).QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 7 {
		t.Errorf("schema version after the start = %d (%v), want 7", version, err)
	}
	invoke(t, b, `{"op":"show","args":{"id":"EXA-1"}}`).wantResult(t, "show from the migrated store", schema.IssueSchemaVersion)
	if migrated := servetest.HasChange(d.DaemonLog(t), "store_migrated"); !migrated {
		t.Errorf("the daemon log has no store_migrated line: %v", d.DaemonLog(t))
	}
	time.Sleep(time.Second)
	if calls := d.Shim.Calls(); len(calls) != 0 {
		t.Errorf("a migrated store served EXA-1 with %d origin calls: %v", len(calls), calls)
	}
}
