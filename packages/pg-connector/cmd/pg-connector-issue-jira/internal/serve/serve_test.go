package serve_test

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
)

func TestParseHooks(t *testing.T) {
	h, err := serve.ParseHooks("retry_delay=1s, poll_interval=20ms,migration_delay=2s,sweep_interval=3m,parent_watch=250ms")
	if err != nil {
		t.Fatal(err)
	}
	want := serve.Hooks{RetryDelay: time.Second, PollInterval: 20 * time.Millisecond, MigrationDelay: 2 * time.Second, SweepInterval: 3 * time.Minute, ParentWatch: 250 * time.Millisecond}
	if h != want {
		t.Errorf("hooks = %+v, want %+v", h, want)
	}
	if h, err := serve.ParseHooks(""); err != nil || h != (serve.Hooks{}) {
		t.Errorf("the empty string must be the zero hooks: %+v, %v", h, err)
	}
	for _, bad := range []string{"retry_delay", "retry_delay=soon", "retry_delay=-1s", "viewer_interval=1s", "poll_interval=0s"} {
		if _, err := serve.ParseHooks(bad); err == nil {
			t.Errorf("ParseHooks(%q) must fail", bad)
		}
	}
}

func TestRunAnOpSentDuringADelayedMigrationAnswersMigratingAndThenSucceeds(t *testing.T) {
	r := newRig(t, fakePjiraOK, "", nil)
	d := r.start(serve.Hooks{MigrationDelay: 1500 * time.Millisecond})

	// The socket accepts at once (a supervisor's health check connects), and the
	// closed gate answers unavailable with a migrating: message.
	var code, msg string
	until(t, 15*time.Second, "the daemon to accept a connection and answer", func() bool {
		resp, err := r.call(serve.Instance, "status", map[string]any{})
		if err != nil {
			return false
		}
		if resp.Error != nil {
			code, msg = resp.Error.Code, resp.Error.Message
		}
		return true
	})
	if code != "unavailable" || !strings.HasPrefix(msg, "migrating:") {
		t.Fatalf("during the migration want unavailable beginning migrating:, got %q %q", code, msg)
	}

	d.waitReady()
	if resp := r.mustCall(serve.Instance, "status", map[string]any{}); resp.Error != nil {
		t.Fatalf("status after the gate opened: %+v", resp.Error)
	}
	// A read through the origin works too: the fake pjira answers the search.
	resp := r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"})
	if resp.Error != nil || !strings.Contains(string(resp.Result), "ALPHA-1") {
		t.Fatalf("show ALPHA-1 = %s, %+v", resp.Result, resp.Error)
	}
	if !hasChange(r.daemonLog(), "store_migrated") {
		t.Error("the migration must be logged as store_migrated")
	}
	if c := d.stop(); c != serve.ExitOK {
		t.Errorf("exit = %d, want %d; stderr: %s", c, serve.ExitOK, d.stderr.String())
	}
}

func TestRunAStoreOfANewerSchemaIsRefusedAndNeverTouched(t *testing.T) {
	var before [32]byte
	r := newRig(t, fakePjiraOK, "", func(r *rig) {
		// A store a newer binary wrote: schema version 99.
		db, err := sql.Open("sqlite", "file:"+r.storePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE from_the_future (x INTEGER); PRAGMA user_version = 99`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(r.storePath())
		if err != nil {
			t.Fatal(err)
		}
		before = sha256.Sum256(raw)
	})
	d := r.start(serve.Hooks{})
	if code := d.wait(30 * time.Second); code != serve.ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, serve.ExitRefused, d.stderr.String())
	}
	if !strings.Contains(d.stderr.String(), "99") {
		t.Errorf("stderr must say why: %q", d.stderr.String())
	}
	if !hasChange(r.daemonLog(), "store_refused") {
		t.Error("the refusal must be logged as store_refused")
	}
	raw, err := os.ReadFile(r.storePath())
	if err != nil {
		t.Fatalf("the store file must still be in place: %v", err)
	}
	if sha256.Sum256(raw) != before {
		t.Error("the refused store was modified")
	}
	entries, err := os.ReadDir(r.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "store.sqlite") && e.Name() != "store.sqlite" {
			t.Errorf("unexpected file beside the refused store: %s", e.Name())
		}
	}
	if _, err := os.Lstat(r.cfg.SocketPath); !os.IsNotExist(err) {
		t.Errorf("the refusing daemon must remove its socket: %v", err)
	}
}

func TestRunACorruptStoreIsMovedAsideAndTheDaemonStartsEmptyWithExpiredCursors(t *testing.T) {
	garbage := []byte(strings.Repeat("this is not a sqlite database; ", 200))
	r := newRig(t, fakePjiraOK, "", func(r *rig) {
		if err := os.WriteFile(r.storePath(), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	d := r.start(serve.Hooks{})
	d.waitReady()

	moved, err := filepath.Glob(r.storePath() + ".moved-aside-*")
	if err != nil || len(moved) != 1 {
		t.Fatalf("want exactly one moved-aside store, got %v (%v)", moved, err)
	}
	if got, err := os.ReadFile(moved[0]); err != nil || string(got) != string(garbage) {
		t.Errorf("the moved-aside file must be the corrupt store, untouched (%v)", err)
	}
	if !hasChange(r.daemonLog(), "store_moved_aside") {
		t.Error("the move-aside must be logged as store_moved_aside")
	}
	// The store was lost with its change log and every cursor, so the first poll
	// of every consumer answers cursor_expired:.
	for _, consumer := range []string{"alpha", "beta"} {
		resp := r.mustCall(serve.Instance, "changes", map[string]any{"consumer": consumer, "cached": true})
		if resp.Error == nil || !strings.HasPrefix(resp.Error.Message, "cursor_expired:") {
			t.Errorf("first poll of %s = %+v / %s, want cursor_expired:", consumer, resp.Error, resp.Result)
		}
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}

func TestRunASecondDaemonOnTheSameStateDirectoryExitsFiveNamingTheLock(t *testing.T) {
	r := newRig(t, fakePjiraOK, "", nil)
	first := r.start(serve.Hooks{})
	first.waitReady()

	second := r.start(serve.Hooks{})
	if code := second.wait(30 * time.Second); code != serve.ExitRefused {
		t.Fatalf("second serve exit = %d, want %d; stderr: %s", code, serve.ExitRefused, second.stderr.String())
	}
	if !strings.Contains(second.stderr.String(), "daemon.lock") {
		t.Errorf("the refusal must name the lock: %q", second.stderr.String())
	}
	// The first daemon is unharmed.
	if resp := r.mustCall(serve.Instance, "status", map[string]any{}); resp.Error != nil {
		t.Errorf("the first daemon stopped answering: %+v", resp.Error)
	}
}

func TestRunAnUnreachableOriginStillStartsAndServesTheStoredContent(t *testing.T) {
	r := newRig(t, fakePjiraDown, "", nil)
	d := r.start(serve.Hooks{})
	d.waitReady()
	resp := r.mustCall(serve.Instance, "status", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("status of a daemon whose origin is down: %+v", resp.Error)
	}
	// A read of an issue that is not stored needs the origin, and says so.
	resp = r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"})
	if resp.Error == nil {
		t.Errorf("show with the origin down and nothing stored must fail, got %s", resp.Result)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}
