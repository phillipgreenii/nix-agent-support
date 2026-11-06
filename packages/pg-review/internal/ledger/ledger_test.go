package ledger

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func open(t *testing.T) *Ledger {
	t.Helper()
	l, err := OpenPath(ctx, filepath.Join(t.TempDir(), "state", "pg-review", "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOpenModesWALAndDefaultPath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "pg-review")
	if err := os.MkdirAll(dir, 0o755); err != nil { // looser pre-existing dir
		t.Fatal(err)
	}
	l, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Path() != filepath.Join(dir, "ledger.db") {
		t.Fatalf("path %s", l.Path())
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %o", m)
	}
	if m := mode(t, l.Path()); m != 0o600 {
		t.Errorf("file mode %o", m)
	}
	var jm string
	if err := l.DB().QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil || jm != "wal" {
		t.Errorf("journal_mode %q %v", jm, err)
	}
	var bt int
	if err := l.DB().QueryRow("PRAGMA busy_timeout").Scan(&bt); err != nil || bt != BusyTimeoutMillis {
		t.Errorf("busy_timeout %d %v", bt, err)
	}
	if v, _ := l.userVersion(ctx); v != SchemaVersion() {
		t.Errorf("user_version %d", v)
	}
}

func TestStateDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/synthetic-home")
	d, err := StateDir()
	if err != nil || d != "/tmp/synthetic-home/.local/state/pg-review" {
		t.Fatalf("%s %v", d, err)
	}
}

func TestNoQuestionOrOutboxTables(t *testing.T) {
	l := open(t)
	rows, err := l.DB().Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		have[n] = true
	}
	for _, want := range []string{"subject", "topic", "revision", "occurrence", "decision", "view", "run", "run_lock", "lens_resume", "meta"} {
		if !have[want] {
			t.Errorf("missing table %s", want)
		}
	}
	for n := range have {
		if strings.Contains(n, "question") || strings.Contains(n, "outbox") {
			t.Errorf("unexpected table %s", n)
		}
	}
}

func TestRunTableColumns(t *testing.T) {
	l := open(t)
	rows, err := l.DB().Query(`PRAGMA table_info(run)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var nn, pk int
		var dflt sql.NullString
		_ = rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk)
		have[name] = true
	}
	for _, c := range []string{
		"staging_path", "session_id", "skip_reason", "profile", "model", "prompt_hash", "ended_at",
		"query_count", "repair_count", "items_filed", "items_deduplicated", "items_skipped", "schedule_id", "analyst",
		"evidence_dir", "pid", "started_at", "lens", "scope", "window_from", "window_to", "run_key", "trigger", "state", "elapsed_minutes",
	} {
		if !have[c] {
			t.Errorf("run column %s missing", c)
		}
	}
}

func TestTopicIDIsMechanical(t *testing.T) {
	a := TopicID("f.md", "k", "kind")
	if len(a) != 64 || a != strings.ToLower(a) {
		t.Fatalf("bad id %q", a)
	}
	if a != TopicID("f.md", "k", "kind") {
		t.Error("not deterministic")
	}
	// NUL separation: moving a character across a field boundary changes the id.
	if TopicID("ab", "c", "d") == TopicID("a", "bc", "d") {
		t.Error("fields not separated")
	}
}

func newRun(t *testing.T, l *Ledger, lens, scope string) Run {
	t.Helper()
	var r Run
	err := l.WithTx(ctx, func(tx *Tx) error {
		var err error
		r, err = tx.InsertRun(ctx, Run{Lens: lens, Scope: scope, PID: 42, StartedAt: time.Unix(1000, 0)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newTopic(t *testing.T, l *Ledger, lens, subject string, waiting bool) Topic {
	t.Helper()
	var tp Topic
	err := l.WithTx(ctx, func(tx *Tx) error {
		var err error
		tp, err = tx.CreateTopic(ctx, NewTopic{DefinedIn: "a.md", SubjectKey: subject, Kind: "stale", Lens: lens, Waiting: waiting})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func TestUniqueTopicAndIndexRejectDuplicates(t *testing.T) {
	l := open(t)
	newTopic(t, l, "lens", "s1", false)
	err := l.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.CreateTopic(ctx, NewTopic{DefinedIn: "a.md", SubjectKey: "s1", Kind: "stale", Lens: "lens"})
		return err
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
	// idempotency key unique index
	tp := newTopic(t, l, "lens", "s2", false)
	_, err = l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingAdopted, BeadIdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	tp3 := newTopic(t, l, "lens", "s3", false)
	_, err = l.Rule(ctx, Ruling{TopicID: tp3.ID, Ruling: RulingAdopted, BeadIdempotencyKey: "k1"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate for idempotency key, got %v", err)
	}
	// duplicate occurrence for the same run
	run := newRun(t, l, "lens", "sc")
	add := func() error {
		return l.WithTx(ctx, func(tx *Tx) error { return tx.AddOccurrence(ctx, tp.ID, run.ID, "sc", "lens", "", "") })
	}
	if err := add(); err != nil {
		t.Fatal(err)
	}
	if err := add(); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate for occurrence, got %v", err)
	}
}

func TestRollbackLeavesNothing(t *testing.T) {
	l := open(t)
	boom := errors.New("boom")
	err := l.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateTopic(ctx, NewTopic{DefinedIn: "a.md", SubjectKey: "s", Kind: "k", Lens: "l"}); err != nil {
			return err
		}
		if _, err := tx.InsertRun(ctx, Run{Lens: "l", Scope: "s"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	for _, tb := range []string{"topic", "subject", "run", "meta"} {
		var n int
		if err := l.DB().QueryRow("SELECT COUNT(*) FROM " + tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after rollback", tb, n)
		}
	}
}

func TestPanicRollsBack(t *testing.T) {
	l := open(t)
	func() {
		defer func() { _ = recover() }()
		_ = l.WithTx(ctx, func(tx *Tx) error {
			_, _ = tx.CreateTopic(ctx, NewTopic{DefinedIn: "a", SubjectKey: "s", Kind: "k", Lens: "l"})
			panic("x")
		})
	}()
	var n int
	_ = l.DB().QueryRow("SELECT COUNT(*) FROM topic").Scan(&n)
	if n != 0 {
		t.Errorf("topic rows %d", n)
	}
	// the connection must not be left in a transaction: a new tx works
	newTopic(t, l, "l", "s2", false)
}

func TestMigrationFromPriorVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d", "ledger.db")
	v1 := []migration{{version: 1, up: func(ctx context.Context, tx *Tx) error {
		_, err := tx.exec(ctx, `CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
			CREATE TABLE legacy(id INTEGER PRIMARY KEY, name TEXT);
			INSERT INTO legacy(name) VALUES('kept')`)
		return err
	}}}
	l, err := openWith(ctx, path, v1)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	v2 := append(append([]migration{}, v1...), migration{version: 2, up: func(ctx context.Context, tx *Tx) error {
		_, err := tx.exec(ctx, `ALTER TABLE legacy ADD COLUMN extra TEXT NOT NULL DEFAULT 'x'`)
		return err
	}})
	l, err = openWith(ctx, path, v2)
	if err != nil {
		t.Fatal(err)
	}
	var name, extra string
	if err := l.DB().QueryRow(`SELECT name, extra FROM legacy`).Scan(&name, &extra); err != nil || name != "kept" || extra != "x" {
		t.Fatalf("%q %q %v", name, extra, err)
	}
	if v, _ := l.userVersion(ctx); v != 2 {
		t.Errorf("version %d", v)
	}
	_ = l.Close()
	// forward-only: an older release refuses a newer database.
	if _, err := openWith(ctx, path, v1); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
}

func TestMigrationFromEmptyDatabaseBuildsCurrentSchema(t *testing.T) {
	l := open(t)
	if _, err := l.DB().Exec(`INSERT INTO meta(key,value) VALUES('x','y')`); err != nil {
		t.Fatal(err)
	}
	p := l.Path()
	_ = l.Close()
	l2, err := OpenPath(ctx, p) // reopen: migrations are idempotent
	if err != nil {
		t.Fatal(err)
	}
	_ = l2.Close()
}

func TestRevisionBumpsOnEveryStateChange(t *testing.T) {
	l := open(t)
	tp := newTopic(t, l, "lens", "s", false)
	run := newRun(t, l, "lens", "sc")
	rev := func() int {
		g, err := l.GetTopic(ctx, tp.ID)
		if err != nil {
			t.Fatal(err)
		}
		return g.Revision
	}
	if rev() != 1 {
		t.Fatalf("initial revision %d", rev())
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// first write-up: revision stays 1; refile with changed content bumps.
	must(l.WithTx(ctx, func(tx *Tx) error { _, err := tx.AddRevision(ctx, tp.ID, run.ID, `{"a":1}`, "r1"); return err }))
	if rev() != 1 {
		t.Errorf("after first write-up %d", rev())
	}
	must(l.WithTx(ctx, func(tx *Tx) error { _, err := tx.AddRevision(ctx, tp.ID, run.ID, `{"a":2}`, "r2"); return err }))
	if rev() != 2 {
		t.Errorf("after refile %d", rev())
	}
	if j, r, err := l.LatestWriteup(ctx, tp.ID); err != nil || j != `{"a":2}` || r != "r2" {
		t.Errorf("writeup %q %q %v", j, r, err)
	}
	must(l.WithTx(ctx, func(tx *Tx) error { return tx.Transition(ctx, tp.ID, StateDeferred) })) // auto-defer
	if rev() != 3 {
		t.Errorf("after auto-defer %d", rev())
	}
	must(l.WithTx(ctx, func(tx *Tx) error { return tx.Transition(ctx, tp.ID, StatePending) })) // reconsideration
	if rev() != 4 {
		t.Errorf("after reconsideration %d", rev())
	}
	_, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingRejected})
	must(err)
	if rev() != 5 {
		t.Errorf("after ruling %d", rev())
	}
	_, err = l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingReopen})
	must(err)
	if g, _ := l.GetTopic(ctx, tp.ID); g.Revision != 6 || g.State != StatePending {
		t.Errorf("after reopen %+v", g)
	}
	// views record the revision seen and go stale on change
	must(l.WithTx(ctx, func(tx *Tx) error { _, err := tx.RecordView(ctx, tp.ID); return err }))
	if v, err := l.ViewRevision(ctx, tp.ID); err != nil || v != 6 {
		t.Errorf("view %d %v", v, err)
	}
	_, err = l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingDeferred})
	must(err)
	if v, _ := l.ViewRevision(ctx, tp.ID); v == rev() {
		t.Error("view should be stale after a ruling")
	}
	if _, err := l.ViewRevision(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestAdoptedRulingStoresPendingBead(t *testing.T) {
	l := open(t)
	tp := newTopic(t, l, "lens", "s", false)
	d, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingAdopted, BeadIdempotencyKey: "key", TargetTracker: "tr", RepoLabel: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if d.BeadState != BeadPending || d.BeadIdempotencyKey != "key" || d.RepoLabel != "repo" {
		t.Errorf("%+v", d)
	}
	r, _ := l.Rule(ctx, Ruling{TopicID: newTopic(t, l, "lens", "s9", false).ID, Ruling: RulingRejected})
	if r.BeadState != BeadNone {
		t.Errorf("bead state %s", r.BeadState)
	}
	if _, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: "Bogus"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
	if _, err := l.Rule(ctx, Ruling{TopicID: "missing", Ruling: RulingRejected}); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCountsAndPromoteWaiting(t *testing.T) {
	l := open(t)
	p := newTopic(t, l, "lens", "p", false)
	w1 := newTopic(t, l, "lens", "w1", true)
	w2 := newTopic(t, l, "lens", "w2", true)
	c, err := l.Counts(ctx)
	if err != nil || c != (Counts{Pending: 1, Waiting: 2}) {
		t.Fatalf("%+v %v", c, err)
	}
	var ids []string
	if err := l.WithTx(ctx, func(tx *Tx) error { var e error; ids, e = tx.PromoteWaiting(ctx, 2); return e }); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != w1.ID {
		t.Fatalf("promoted %v, want oldest %s", ids, w1.ID)
	}
	if g, _ := l.GetTopic(ctx, w2.ID); g.State != StateWaiting {
		t.Errorf("w2 %s", g.State)
	}
	_, _ = l.Rule(ctx, Ruling{TopicID: p.ID, Ruling: RulingRejected})
	_, _ = l.db.Exec(`UPDATE decision SET bead_state='filing'`)
	c, _ = l.Counts(ctx)
	if c.Pending != 1 || c.Waiting != 1 || c.Filing != 1 {
		t.Errorf("%+v", c)
	}
}

func TestRunRoundTripAndLock(t *testing.T) {
	l := open(t)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var r Run
	err := l.WithTx(ctx, func(tx *Tx) error {
		var err error
		r, err = tx.InsertRun(ctx, Run{Lens: "l", Scope: "s", RunKey: "l/s/w/e", PID: 77, StartedAt: start, Analyst: "ccpool", EvidenceDir: "/x"})
		if err != nil {
			return err
		}
		return tx.AcquireLock(ctx, "l", "s", r.ID, 77, start)
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != RunRunning || r.PID != 77 || !r.StartedAt.Equal(start) {
		t.Fatalf("%+v", r)
	}
	lk, err := l.GetLock(ctx, "l", "s")
	if err != nil || lk.RunID != r.ID || lk.PID != 77 || !lk.StartedAt.Equal(start) {
		t.Fatalf("%+v %v", lk, err)
	}
	err = l.WithTx(ctx, func(tx *Tx) error { return tx.AcquireLock(ctx, "l", "s", r.ID, 78, start) })
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("want ErrLockBusy, got %v", err)
	}
	// a different scope is free
	if err := l.WithTx(ctx, func(tx *Tx) error { return tx.AcquireLock(ctx, "l", "other", r.ID, 78, start) }); err != nil {
		t.Fatal(err)
	}
	if ls, _ := l.Locks(ctx); len(ls) != 2 {
		t.Errorf("locks %d", len(ls))
	}
	r.State, r.ItemsFiled, r.EndedAt = RunFiled, 3, start.Add(time.Hour)
	if err := l.WithTx(ctx, func(tx *Tx) error {
		if err := tx.UpdateRun(ctx, r); err != nil {
			return err
		}
		return tx.ReleaseLock(ctx, "l", "s")
	}); err != nil {
		t.Fatal(err)
	}
	g, err := l.GetRun(ctx, r.ID)
	if err != nil || g.State != RunFiled || g.ItemsFiled != 3 || !g.EndedAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("%+v %v", g, err)
	}
	if k, err := l.LastRunForKey(ctx, "l/s/w/e"); err != nil || k.ID != r.ID {
		t.Errorf("%+v %v", k, err)
	}
	if _, err := l.GetLock(ctx, "l", "s"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := l.GetRun(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if err := l.WithTx(ctx, func(tx *Tx) error { return tx.UpdateRun(ctx, Run{ID: 999, State: RunFailed}) }); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestHeartbeat(t *testing.T) {
	l := open(t)
	if _, err := l.Heartbeat(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := l.WithTx(ctx, func(tx *Tx) error { return tx.SetHeartbeat(ctx, at) }); err != nil {
		t.Fatal(err)
	}
	if g, err := l.Heartbeat(ctx); err != nil || !g.Equal(at) {
		t.Fatalf("%v %v", g, err)
	}
}

func TestIntegrityCheck(t *testing.T) {
	l := open(t)
	if err := l.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	// corrupt a copy on disk, then open it directly
	newTopic(t, l, "lens", "s", false)
	bad := filepath.Join(t.TempDir(), "bad.db")
	if err := l.Backup(ctx, bad); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096; i < len(b); i++ { // wipe every page after the first
		b[i] = 0xFF
	}
	if err := os.WriteFile(bad, b, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", bad)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cl := &Ledger{db: db, path: bad, now: time.Now}
	if err := cl.IntegrityCheck(ctx); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
}

func TestExportJSONLAndFile(t *testing.T) {
	l := open(t)
	tp := newTopic(t, l, "lens", "s", false)
	var buf bytes.Buffer
	if err := l.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var hdr map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil || hdr["export"] != "pg-review-ledger" || int(hdr["schema_version"].(float64)) != SchemaVersion() {
		t.Fatalf("header %v %v", hdr, err)
	}
	found := false
	for _, ln := range lines[1:] {
		var e struct {
			Table string         `json:"table"`
			Row   map[string]any `json:"row"`
		}
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("%q: %v", ln, err)
		}
		if e.Table == "topic" && e.Row["topic_id"] == tp.ID && e.Row["state"] == StatePending {
			found = true
		}
	}
	if !found {
		t.Error("topic row not exported")
	}
	dest := filepath.Join(filepath.Dir(l.Path()), "export.jsonl")
	if err := l.ExportFile(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if mode(t, dest) != 0o600 {
		t.Errorf("export mode %o", mode(t, dest))
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, buf.Bytes()) {
		t.Error("file export differs from stream export")
	}
}

func TestBackupIsReadable(t *testing.T) {
	l := open(t)
	tp := newTopic(t, l, "lens", "s", false)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := l.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if mode(t, dest) != 0o600 {
		t.Errorf("mode %o", mode(t, dest))
	}
	b, err := OpenPath(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if g, err := b.GetTopic(ctx, tp.ID); err != nil || g.SubjectKey != "s" {
		t.Fatalf("%+v %v", g, err)
	}
	if err := l.Backup(ctx, dest); !errors.Is(err, ErrInvalid) {
		t.Errorf("existing destination must be refused, got %v", err)
	}
}

func TestRulingsLogAppendOnly(t *testing.T) {
	l := open(t)
	l.SetClock(func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	a := newTopic(t, l, "lens", "a", false)
	b := newTopic(t, l, "lens", "b", false)
	_, _ = l.Rule(ctx, Ruling{TopicID: a.ID, Ruling: RulingRejected, Rationale: "no"})
	_, _ = l.Rule(ctx, Ruling{TopicID: b.ID, Ruling: RulingDeferred, ReconsiderWhen: "2026-06-01"})
	data, err := os.ReadFile(l.RulingsLogPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	var e RulingLogEntry
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil || e.TopicID != b.ID || e.Ruling != RulingDeferred || e.Lens != "lens" || e.ReconsiderWhen != "2026-06-01" {
		t.Fatalf("%+v %v", e, err)
	}
	if mode(t, l.RulingsLogPath()) != 0o600 {
		t.Errorf("mode %o", mode(t, l.RulingsLogPath()))
	}
	// a later append keeps earlier lines
	_, _ = l.Rule(ctx, Ruling{TopicID: a.ID, Ruling: RulingReopen})
	data2, _ := os.ReadFile(l.RulingsLogPath())
	if !strings.HasPrefix(string(data2), string(data)) {
		t.Error("log was rewritten")
	}
}
