package logs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func newStructured(t *testing.T) (*StructuredLog, string) {
	t.Helper()
	dir := t.TempDir()
	l := NewStructuredLog(dir, "v9")
	l.Now = fixedClock(t0)
	return l, DaemonLogPath(dir)
}

// onlyRow returns the single row written and checks the JSONL standard fields
// every line must carry.
func onlyRow(t *testing.T, path, change, level string) map[string]any {
	t.Helper()
	rows := readRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("want exactly one line per state change, got %d: %v", len(rows), rows)
	}
	row := rows[0]
	if row["change"] != change {
		t.Errorf("change = %v, want %v", row["change"], change)
	}
	if ts, _ := row["time"].(string); ts != "2026-10-13T12:00:00.000Z" {
		t.Errorf("time = %v", row["time"])
	} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("time %q is not RFC 3339: %v", ts, err)
	}
	if row["level"] != level {
		t.Errorf("level = %v, want %v", row["level"], level)
	}
	if msg, _ := row["msg"].(string); msg == "" {
		t.Errorf("empty msg: %v", row)
	}
	if row["service"] != "pg-connector-github" || row["version"] != "v9" || row["pid"] != float64(os.Getpid()) {
		t.Errorf("identity fields: %v", row)
	}
	return row
}

func expectFields(t *testing.T, row map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if row[k] != v {
			t.Errorf("%s = %v, want %v (row %v)", k, row[k], v, row)
		}
	}
}

func TestStructuredLogOneLinePerStateChange(t *testing.T) {
	until := t0.Add(10 * time.Minute)
	cases := []struct {
		name   string
		change string
		level  string
		do     func(l *StructuredLog)
		want   map[string]any
	}{
		{
			"start", ChangeDaemonStarted, "info", func(l *StructuredLog) { l.DaemonStarted("/state", "/state/sock", "abc123") },
			map[string]any{"state_dir": "/state", "socket_path": "/state/sock", "config_hash": "abc123"},
		},
		{
			"lock acquired", ChangeLockAcquired, "info", func(l *StructuredLog) { l.LockAcquired("/state/daemon.lock") },
			map[string]any{"lock": "/state/daemon.lock"},
		},
		{
			"lock held", ChangeLockHeld, "warn", func(l *StructuredLog) { l.LockHeld("/state/daemon.lock") },
			map[string]any{"lock": "/state/daemon.lock"},
		},
		{
			"migration", ChangeStoreMigrated, "info", func(l *StructuredLog) { l.Migration("/state/store.sqlite", 0, 3, 1500*time.Millisecond) },
			map[string]any{"store": "/state/store.sqlite", "from_version": float64(0), "to_version": float64(3), "took": "1.5s"},
		},
		{
			"viewer change", ChangeViewerChanged, "info", func(l *StructuredLog) { l.ViewerChanged("github.com", "alice", "bob") },
			map[string]any{"host": "github.com", "old_login": "alice", "new_login": "bob"},
		},
		{
			"budget pause", ChangeBudgetPaused, "warn", func(l *StructuredLog) { l.BudgetPaused(governor.BucketGraphQL, "secondary limit", until) },
			map[string]any{"bucket": "graphql", "reason": "secondary limit", "until": "2026-10-13T12:10:00.000Z"},
		},
		{
			"budget resume", ChangeBudgetResumed, "info", func(l *StructuredLog) { l.BudgetResumed(governor.BucketRESTCore, "secondary limit") },
			map[string]any{"bucket": "rest_core", "reason": "secondary limit"},
		},
		{
			"pause via the governor callback", ChangeBudgetPaused, "warn", func(l *StructuredLog) { l.OnPauseChange(governor.BucketRESTSearch, "abuse", until) },
			map[string]any{"bucket": "rest_search", "reason": "abuse"},
		},
		{
			"resume via the governor callback", ChangeBudgetResumed, "info", func(l *StructuredLog) { l.OnPauseChange(governor.BucketRESTSearch, "abuse", time.Time{}) },
			map[string]any{"bucket": "rest_search", "reason": "abuse"},
		},
		{"store move-aside", ChangeStoreMovedAside, "warn", func(l *StructuredLog) {
			l.StoreMovedAside("/state/store.sqlite", "/state/store.sqlite.moved-aside-x", errors.New("database disk image is malformed"))
		}, map[string]any{"store": "/state/store.sqlite", "moved_to": "/state/store.sqlite.moved-aside-x", "cause": "database disk image is malformed"}},
		{
			"store refusal", ChangeStoreRefused, "error", func(l *StructuredLog) { l.StoreRefused("/state/store.sqlite", 9, 3) },
			map[string]any{"store": "/state/store.sqlite", "found_version": float64(9), "supported_version": float64(3)},
		},
		{"store opened: migrated", ChangeStoreMigrated, "info", func(l *StructuredLog) {
			l.StoreOpened("/s", store.Result{Outcome: store.OutcomeMigrated, FromVersion: 1, ToVersion: 3}, nil, time.Second)
		}, map[string]any{"from_version": float64(1), "to_version": float64(3)}},
		{"store opened: moved aside", ChangeStoreMovedAside, "warn", func(l *StructuredLog) {
			l.StoreOpened("/s", store.Result{Outcome: store.OutcomeMovedAside, MovedAsideTo: "/s.aside", Cause: errors.New("corrupt")}, nil, 0)
		}, map[string]any{"moved_to": "/s.aside", "cause": "corrupt"}},
		{"store opened: refused", ChangeStoreRefused, "error", func(l *StructuredLog) {
			err := &store.SchemaNewerError{Path: "/s", Found: 7, Supported: 3}
			l.StoreOpened("/s", store.Result{Outcome: store.OutcomeRefusedNewer, ToVersion: 3}, err, 0)
		}, map[string]any{"found_version": float64(7), "supported_version": float64(3)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, path := newStructured(t)
			c.do(l)
			expectFields(t, onlyRow(t, path, c.change, c.level), c.want)
		})
	}
}

func TestAnOpenedStoreIsNotAStateChange(t *testing.T) {
	l, path := newStructured(t)
	l.StoreOpened("/s", store.Result{Outcome: store.OutcomeOpened, FromVersion: 3, ToVersion: 3}, nil, 0)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an unchanged store logs nothing: %v", err)
	}
}

// The structured log is the connectord.Logger the library components log into.
func TestStructuredLogIsTheLibraryLogger(t *testing.T) {
	l, path := newStructured(t)
	var lg connectord.Logger = l
	lg.Log("shell_started", "state_dir", "/state", "socket_path", "/state/sock")
	row := onlyRow(t, path, "shell_started", "info")
	expectFields(t, row, map[string]any{"state_dir": "/state", "socket_path": "/state/sock", "msg": "shell started"})
}

func TestGenericLogLevelsAndFieldHandling(t *testing.T) {
	for change, level := range map[string]string{
		"lock_held": "warn", "drain_hook_failed": "warn", "peer_uid_rejected": "warn", "drain_timeout": "warn",
		"shell_started": "info", "drain_done": "info", "viewer_changed": "info", "auth_state_changed": "info",
	} {
		l, path := newStructured(t)
		l.Log(change)
		onlyRow(t, path, change, level)
	}

	l, path := newStructured(t)
	l.Log("odd", "level", "debug", "msg", "hijack", "err", errors.New("boom"), "wait", 2*time.Second, "n", 3, "dangling")
	row := onlyRow(t, path, "odd", "info")
	expectFields(t, row, map[string]any{
		"field_level": "debug", "field_msg": "hijack", "err": "boom", "wait": "2s", "n": float64(3), "msg": "odd",
	})
	if v, ok := row["dangling"]; !ok || v != nil {
		t.Errorf("a trailing key keeps a null value: %v", row)
	}
}

func TestStructuredLogIsConcurrencySafeAndOneLinePerCall(t *testing.T) {
	l, path := newStructured(t)
	done := make(chan struct{})
	const n = 50
	for i := 0; i < n; i++ {
		go func() { l.Log("daemon_started", "i", i); done <- struct{}{} }()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if rows := readRows(t, path); len(rows) != n {
		t.Fatalf("%d lines, want %d", len(rows), n)
	}
}

func TestStructuredLogFailureNeverPanics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewStructuredLog(filepath.Join(dir, "blocker"), "v")
	l.Log("daemon_started") // the state directory is a file: swallowed
	l.LockHeld("/x")
}
