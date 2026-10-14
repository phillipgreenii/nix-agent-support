package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fastOpts keeps fsyncs off: durability is meaningless for a database
// deleted at test exit. The crash-recovery test does NOT use it.
func fastOpts() Options { return Options{Synchronous: "OFF"} }

func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sub", "store.db")
}

// currentTables is the table set of the current schema version, sorted as
// tableNames returns it. A migration that adds a table changes this list.
var currentTables = []string{
	"alias", "budget", "change_log", "consumer", "entity", "entity_group", "job", "pending_change", "query", "store_meta",
}

func mustOpen(t *testing.T, path string, opts Options) (*Store, Result) {
	t.Helper()
	s, res, err := Open(path, opts)
	if err != nil {
		t.Fatalf("Open(%s): %v (result %+v)", path, err, res)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, res
}

func tableNames(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func columnNames(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.DB().Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestSchemaVersionMatchesLadder(t *testing.T) {
	if SchemaVersion != len(builtinMigrations) {
		t.Fatalf("SchemaVersion = %d but the ladder has %d steps", SchemaVersion, len(builtinMigrations))
	}
	if err := validateLadder(builtinMigrations); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLadderRejectsGapsAndNilApply(t *testing.T) {
	ok := func(*sql.Tx, func(string)) error { return nil }
	if err := validateLadder([]Migration{{Version: 2, Name: "x", Apply: ok}}); err == nil {
		t.Error("gap at the start accepted")
	}
	if err := validateLadder([]Migration{{Version: 1, Name: "x"}}); err == nil {
		t.Error("nil Apply accepted")
	}
	if _, _, err := Open(dbPath(t), Options{Migrations: []Migration{{Version: 3, Apply: ok}}}); err == nil {
		t.Error("Open accepted a bad ladder")
	}
}

// TestOpenEmptyCreatesCurrentSchema describes schema version 3: the five version
// 1 tables, job, and the change engine's pending_change, change_log, consumer and
// store_meta, with query.baseline_at. A migration that bumps the version updates this test, its
// table list and its column lists with it.
func TestOpenEmptyCreatesCurrentSchema(t *testing.T) {
	const wantVersion = 3
	if SchemaVersion != wantVersion {
		t.Fatalf("SchemaVersion = %d, this test describes version %d", SchemaVersion, wantVersion)
	}
	path := dbPath(t)
	s, res := mustOpen(t, path, fastOpts())

	if res.Outcome != OutcomeMigrated || res.FromVersion != 0 || res.ToVersion != wantVersion {
		t.Fatalf("result = %+v, want migrated 0 -> %d", res, wantVersion)
	}
	if s.Version() != wantVersion || s.Path() != path {
		t.Errorf("Version/Path = %d %q", s.Version(), s.Path())
	}
	var userVersion int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil || userVersion != wantVersion {
		t.Errorf("user_version = %d err=%v, want %d", userVersion, err, wantVersion)
	}

	if got := tableNames(t, s); !slices.Equal(got, currentTables) {
		t.Errorf("tables = %v, want exactly %v", got, currentTables)
	}
	wantCols := map[string][]string{
		"query": {
			"key", "configured", "ids", "complete", "fetched_at", "fresh_until", "last_success_at", "last_error",
			"baseline_at",
		},
		"alias": {"node_id", "ref", "seen_at"},
		"entity": {
			"kind", "id", "version", "last_access", "keep_alive", "last_known_mergeable",
			"tombstoned_at",
		},
		"entity_group": {
			"kind", "id", "group_name", "content", "fingerprint", "fetched_at", "fresh_until",
			"max_age_at", "last_error", "generation", "fetched_for_head", "fetched_for_base",
		},
		"budget": {"bucket", "at", "remaining", "reset_at", "spent", "purpose"},
		"job":    {"run_id", "attempt", "jobs", "fetched_at"},
		"change_log": {
			"seq", "kind", "id", "version", "head_sha", "kinds", "fields", "origin", "origin_updated_at", "at",
		},
		"pending_change": {
			"kind", "id", "kinds", "fields", "awaited", "head_sha", "base_sha", "origin", "origin_updated_at",
			"detected_at", "deadline",
		},
		"consumer":   {"consumer", "kind", "query", "acked_seq", "seen_at"},
		"store_meta": {"key", "value"},
	}
	for table, cols := range wantCols {
		got := columnNames(t, s, table)
		for _, c := range cols {
			if !slices.Contains(got, c) {
				t.Errorf("table %s lacks column %s (has %v)", table, c, got)
			}
		}
	}

	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestIdempotentSecondOpen(t *testing.T) {
	path := dbPath(t)
	s, _ := mustOpen(t, path, fastOpts())
	ctx := context.Background()
	if err := s.PutAlias(ctx, AliasRow{NodeID: "N1", Ref: "org/repo#1", SeenAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeOpened || res.FromVersion != SchemaVersion || res.ToVersion != SchemaVersion {
		t.Fatalf("second open result = %+v, want opened at version %d", res, SchemaVersion)
	}
	if a, ok, err := s2.AliasByNodeID(ctx, "N1"); err != nil || !ok || a.Ref != "org/repo#1" {
		t.Errorf("alias after reopen = %+v ok=%v err=%v", a, ok, err)
	}
	// Close is idempotent enough for a deferred double close in drain hooks.
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// fixtureVersion extracts N from testdata/vN.sqlite3.
var fixtureName = regexp.MustCompile(`^v(\d+)\.sqlite3$`)

func fixtures(t *testing.T) map[int]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "v*.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, p := range paths {
		m := fixtureName.FindStringSubmatch(filepath.Base(p))
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		out[v] = p
	}
	return out
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureExistsForEveryVersion(t *testing.T) {
	fx := fixtures(t)
	for v := 1; v <= SchemaVersion; v++ {
		if _, ok := fx[v]; !ok {
			t.Errorf("no fixture store testdata/v%d.sqlite3 for schema version %d", v, v)
		}
	}
}

func TestOpenFromEachFixture(t *testing.T) {
	ctx := context.Background()
	for v, fx := range fixtures(t) {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			path := dbPath(t)
			copyFile(t, fx, path)
			s, res := mustOpen(t, path, fastOpts())

			want := OutcomeMigrated
			if v == SchemaVersion {
				want = OutcomeOpened
			}
			if res.Outcome != want || res.FromVersion != v || res.ToVersion != SchemaVersion {
				t.Fatalf("result = %+v, want %v from %d to %d", res, want, v, SchemaVersion)
			}
			assertFixtureData(t, ctx, s)
			assertFixtureJobs(t, ctx, s, v)
			assertFixtureChanges(t, ctx, s, v)
		})
	}
}

// TestUpMigrationFromFixtureKeepsData climbs from the version 1 fixture
// through the shipped ladder plus one test-only scratch step and checks the old
// data survived, the shipped job and change-table steps ran, and the scratch step
// ran exactly once.
func TestUpMigrationFromFixtureKeepsData(t *testing.T) {
	fx, ok := fixtures(t)[1]
	if !ok {
		t.Fatal("no version 1 fixture")
	}
	path := dbPath(t)
	copyFile(t, fx, path)

	// The scratch step is defined here and never shipped: it sits one past the
	// shipped ladder (never a literal version: the shipped ladder ends at
	// SchemaVersion).
	scratch := SchemaVersion + 1
	ran := 0
	ladder := append(slices.Clone(builtinMigrations), Migration{
		Version: scratch, Name: "add scratch table",
		Apply: func(tx *sql.Tx, report func(string)) error {
			ran++
			_, err := tx.Exec(`CREATE TABLE scratch (x INTEGER)`)
			return err
		},
	})
	opts := fastOpts()
	opts.Migrations = ladder
	s, res := mustOpen(t, path, opts)
	if res.Outcome != OutcomeMigrated || res.FromVersion != 1 || res.ToVersion != scratch || ran != 1 {
		t.Fatalf("result = %+v ran=%d, want migrated 1 -> %d with one scratch step", res, ran, scratch)
	}
	assertFixtureData(t, context.Background(), s)
	assertFixtureChanges(t, context.Background(), s, 1)
	for _, shipped := range []string{"job", "pending_change", "change_log", "consumer", "store_meta"} {
		if !slices.Contains(tableNames(t, s), shipped) {
			t.Errorf("the shipped step creating %s did not run on the way up", shipped)
		}
	}
	if !slices.Contains(tableNames(t, s), "scratch") {
		t.Errorf("step %d did not create its table", scratch)
	}
	var v int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != scratch {
		t.Errorf("user_version = %d err=%v, want %d", v, err, scratch)
	}

	// Re-run: nothing more to apply.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, res2 := mustOpen(t, path, opts)
	_ = s2
	if res2.Outcome != OutcomeOpened || ran != 1 {
		t.Errorf("re-run result = %+v ran=%d, want opened and no new step", res2, ran)
	}
}

// A store one version past the binary's (the next step a later migration adds)
// is refused exactly like one far past it: ErrSchemaNewer, the file untouched.
func TestNewerSchemaRefusedAndFileUntouched(t *testing.T) {
	for _, found := range []int{SchemaVersion + 1, SchemaVersion + 5} {
		t.Run(fmt.Sprintf("found_%d", found), func(t *testing.T) {
			path := dbPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`CREATE TABLE future (x TEXT)`,
				`INSERT INTO future VALUES ('from the newer binary')`,
				fmt.Sprintf(`PRAGMA user_version = %d`, found),
			} {
				if _, err := raw.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			statBefore, _ := os.Stat(path)
			namesBefore := dirNames(t, filepath.Dir(path))

			s, res, err := Open(path, fastOpts())
			if s != nil {
				_ = s.Close()
				t.Fatal("Open returned a store for a newer schema")
			}
			if !errors.Is(err, ErrSchemaNewer) {
				t.Fatalf("err = %v, want ErrSchemaNewer", err)
			}
			var typed *SchemaNewerError
			if !errors.As(err, &typed) || typed.Found != found || typed.Supported != SchemaVersion || typed.Path != path {
				t.Errorf("typed error = %+v", typed)
			}
			if res.Outcome != OutcomeRefusedNewer || res.MovedAsideTo != "" {
				t.Errorf("result = %+v, want refused_newer and no move", res)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("the refused store's bytes changed")
			}
			statAfter, _ := os.Stat(path)
			if !statAfter.ModTime().Equal(statBefore.ModTime()) {
				t.Error("the refused store's mtime changed")
			}
			if got := dirNames(t, filepath.Dir(path)); !slices.Equal(got, namesBefore) {
				t.Errorf("directory changed: %v -> %v (a move-aside or sidecar appeared)", namesBefore, got)
			}
		})
	}
}

func TestCorruptGarbageIsMovedAside(t *testing.T) {
	path := dbPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := bytes.Repeat([]byte("this is not a database\n"), 200)
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 5, 30, 0, 123456789, time.UTC)

	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	s, res := mustOpen(t, path, opts)

	if res.Outcome != OutcomeMovedAside || res.Cause == nil {
		t.Fatalf("result = %+v, want moved_aside with a cause", res)
	}
	wantName := path + ".moved-aside-20261011T053000.123456789Z"
	if res.MovedAsideTo != wantName {
		t.Errorf("MovedAsideTo = %q, want %q", res.MovedAsideTo, wantName)
	}
	moved, err := os.ReadFile(res.MovedAsideTo)
	if err != nil || !bytes.Equal(moved, garbage) {
		t.Errorf("moved-aside content differs or unreadable: %v", err)
	}
	if res.ToVersion != SchemaVersion || s.Version() != SchemaVersion {
		t.Errorf("fresh store at version %d", s.Version())
	}
	if got := tableNames(t, s); !slices.Equal(got, currentTables) {
		t.Errorf("fresh store tables = %v, want %v", got, currentTables)
	}
}

func TestCorruptBodyIsMovedAside(t *testing.T) {
	path := dbPath(t)
	s, _ := mustOpen(t, path, fastOpts())
	ctx := context.Background()
	// Enough rows to span many pages.
	for i := 0; i < 400; i++ {
		g := GroupRow{Kind: "k", ID: fmt.Sprint(i), Group: "summary", Content: bytes.Repeat([]byte{byte(i)}, 900)}
		if err := s.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 8*4096 {
		t.Fatalf("test database too small (%d bytes)", len(b))
	}
	// Keep the valid header and page 1; wreck everything after it.
	for i := 4096 * 2; i < len(b); i++ {
		b[i] = 0xA5
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	s2, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeMovedAside {
		t.Fatalf("result = %+v, want moved_aside", res)
	}
	if _, err := os.Stat(res.MovedAsideTo); err != nil {
		t.Errorf("moved-aside file missing: %v", err)
	}
	if _, ok, err := s2.GetGroup(ctx, "k", "1", "summary"); err != nil || ok {
		t.Errorf("fresh store still has old rows: ok=%v err=%v", ok, err)
	}
}

func TestFailedMigrationMovesStoreAside(t *testing.T) {
	path := dbPath(t)
	{
		s, _ := mustOpen(t, path, fastOpts())
		if err := s.PutAlias(context.Background(), AliasRow{NodeID: "N1", Ref: "org/repo#1", SeenAt: time.UnixMilli(5)}); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}

	failures := 0
	// The failing step sits one past the shipped ladder; the store written
	// above is at the shipped top, SchemaVersion.
	failing := SchemaVersion + 1
	ladder := append(slices.Clone(builtinMigrations), Migration{
		Version: failing, Name: "fails halfway once",
		Apply: func(tx *sql.Tx, report func(string)) error {
			if _, err := tx.Exec(`CREATE TABLE half_done (x INTEGER)`); err != nil {
				return err
			}
			if failures == 0 {
				failures++
				return errors.New("boom midway through the step")
			}
			return nil
		},
	})
	opts := fastOpts()
	opts.Migrations = ladder
	s, res := mustOpen(t, path, opts)

	if res.Outcome != OutcomeMovedAside || res.MovedAsideTo == "" {
		t.Fatalf("result = %+v, want moved_aside", res)
	}
	if res.Cause == nil || !strings.Contains(res.Cause.Error(), "boom midway") || !errors.Is(res.Cause, errMigrationFailed) {
		t.Errorf("cause = %v", res.Cause)
	}
	if res.FromVersion != 0 || res.ToVersion != failing || s.Version() != failing {
		t.Errorf("fresh store: result %+v version %d, want %d", res, s.Version(), failing)
	}
	// The fresh store has no old rows ...
	if _, ok, _ := s.AliasByNodeID(context.Background(), "N1"); ok {
		t.Error("fresh store kept the old alias")
	}
	// ... and the moved-aside copy kept them, at the old version, with the
	// failed step rolled back.
	old, err := sql.Open("sqlite", "file:"+res.MovedAsideTo+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	var n, v int
	if err := old.QueryRow(`SELECT count(*) FROM alias`).Scan(&n); err != nil || n != 1 {
		t.Errorf("moved-aside alias rows = %d err=%v", n, err)
	}
	if err := old.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Errorf("moved-aside user_version = %d err=%v, want %d", v, err, SchemaVersion)
	}
	var half int
	if err := old.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&half); err != nil || half != 0 {
		t.Errorf("the failed step was not rolled back (half_done present=%d err=%v)", half, err)
	}
}

func TestMovedAsideExpiry(t *testing.T) {
	path := dbPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string {
		return path + ".moved-aside-" + now.Add(-d).Format(movedAsideLayout)
	}
	old, young := stamp(3*time.Hour), stamp(30*time.Minute)
	unparsable := path + ".moved-aside-not-a-time"
	other := filepath.Join(filepath.Dir(path), "unrelated.db")
	for _, p := range []string{old, old + "-wal", old + "-shm", young, young + "-wal", unparsable, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	opts.Expiry = time.Hour
	_, res := mustOpen(t, path, opts)

	if res.Outcome != OutcomeMigrated {
		t.Errorf("outcome = %v, want migrated (prune must not disturb the open)", res.Outcome)
	}
	if !slices.Equal(res.Pruned, []string{old}) {
		t.Errorf("Pruned = %v, want [%s]", res.Pruned, old)
	}
	for _, gone := range []string{old, old + "-wal", old + "-shm"} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be deleted (err=%v)", gone, err)
		}
	}
	for _, kept := range []string{young, young + "-wal", unparsable, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should be kept: %v", kept, err)
		}
	}
}

func TestMovedAsideCollisionGetsDistinctName(t *testing.T) {
	path := dbPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
		to, err := moveAside(path, now)
		if err != nil {
			t.Fatal(err)
		}
		if seen[to] {
			t.Fatalf("name %s reused", to)
		}
		seen[to] = true
	}
}

// ---- WAL crash recovery ------------------------------------------------

const crashEnv = "PG_CONNECTOR_STORE_TEST_CRASH_WRITER"

// TestCrashWriterHelper is not a test: the parent re-executes the test
// binary with crashEnv set, and this function writes through the real Open
// path and then kills its own process with SIGKILL, with no Checkpoint and
// no Close.
func TestCrashWriterHelper(t *testing.T) {
	path := os.Getenv(crashEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	opts := Options{} // default synchronous: commits are durable
	if v := os.Getenv(crashEnv + "_LADDER"); v != "" {
		n, _ := strconv.Atoi(v)
		opts.Migrations = testLadder(n)
	}
	s, _, err := Open(path, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper open:", err)
		os.Exit(3)
	}
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := s.PutAlias(ctx, AliasRow{
			NodeID: fmt.Sprintf("N%d", i), Ref: fmt.Sprintf("org/repo#%d", i),
			SeenAt: time.UnixMilli(int64(1000 + i)),
		}); err != nil {
			fmt.Fprintln(os.Stderr, "helper put:", err)
			os.Exit(4)
		}
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {} // unreachable; SIGKILL is not catchable
}

// testLadder is the builtin ladder extended to n steps with no-op steps.
func testLadder(n int) []Migration {
	l := slices.Clone(builtinMigrations)
	for v := len(l) + 1; v <= n; v++ {
		v := v
		l = append(l, Migration{
			Version: v, Name: fmt.Sprintf("test step %d", v),
			Apply: func(tx *sql.Tx, _ func(string)) error {
				_, err := tx.Exec(fmt.Sprintf(`CREATE TABLE test_step_%d (x INTEGER)`, v))
				return err
			},
		})
	}
	return l[:n]
}

func runCrashWriter(t *testing.T, path string, ladder int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashWriterHelper$", "-test.v")
	cmd.Env = append(os.Environ(), crashEnv+"="+path)
	if ladder > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s_LADDER=%d", crashEnv, ladder))
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("helper should have been killed, got err=%v output=%s", err, out)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("helper did not die by SIGKILL: %v output=%s", err, out)
	}
}

func TestWALCrashRecovery(t *testing.T) {
	path := dbPath(t)
	runCrashWriter(t, path, 0)

	// The writer never checkpointed: the rows live only in the WAL.
	fi, err := os.Stat(path + "-wal")
	if err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL left by the killed writer (err=%v)", err)
	}

	s, res := mustOpen(t, path, Options{})
	if res.Outcome != OutcomeOpened {
		t.Errorf("result = %+v, want opened (recovery is not a migration)", res)
	}
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		a, ok, err := s.AliasByNodeID(ctx, fmt.Sprintf("N%d", i))
		if err != nil || !ok || a.Ref != fmt.Sprintf("org/repo#%d", i) {
			t.Fatalf("alias N%d after crash: %+v ok=%v err=%v", i, a, ok, err)
		}
	}
}

// A newer binary that crashed leaves its schema bump only in the WAL, so
// the main file's header still looks old. Open must still refuse, and must
// not move the store aside.
func TestNewerSchemaInWALIsRefusedNotMovedAside(t *testing.T) {
	path := dbPath(t)
	runCrashWriter(t, path, SchemaVersion+1)

	s, res, err := Open(path, Options{})
	if s != nil {
		_ = s.Close()
	}
	if !errors.Is(err, ErrSchemaNewer) || res.Outcome != OutcomeRefusedNewer {
		t.Fatalf("err=%v result=%+v, want refused_newer", err, res)
	}
	var typed *SchemaNewerError
	if !errors.As(err, &typed) || typed.Found != SchemaVersion+1 {
		t.Errorf("typed = %+v", typed)
	}
	for _, n := range dirNames(t, filepath.Dir(path)) {
		if strings.Contains(n, "moved-aside") {
			t.Errorf("store was moved aside: %s", n)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("main file gone: %v", err)
	}
}

func TestCheckpointTruncatesWAL(t *testing.T) {
	path := dbPath(t)
	s, _ := mustOpen(t, path, fastOpts())
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if err := s.PutAlias(ctx, AliasRow{NodeID: fmt.Sprint("N", i), Ref: "org/repo#1", SeenAt: time.UnixMilli(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected WAL content before checkpoint (err=%v)", err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("WAL has %d bytes after Checkpoint", fi.Size())
	}
	if _, ok, _ := s.AliasByNodeID(ctx, "N7"); !ok {
		t.Error("data lost by Checkpoint")
	}
}

// ---- observable migration ---------------------------------------------

func TestMigratingIsObservableWhileASlowMigrationRuns(t *testing.T) {
	path := dbPath(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	slow := Migration{
		Version: 1, Name: "slow test step",
		Apply: func(tx *sql.Tx, report func(string)) error {
			report("copying row 7 of 100")
			close(entered)
			<-release
			_, err := tx.Exec(`CREATE TABLE slow (x INTEGER)`)
			return err
		},
	}
	progress := NewProgress()
	if st := progress.Snapshot(); st.Migrating || st.Message() != "" {
		t.Fatalf("idle progress = %+v", st)
	}

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		opts := fastOpts()
		opts.Migrations = []Migration{slow}
		opts.Progress = progress
		s, res, err := Open(path, opts)
		if s != nil {
			_ = s.Close()
		}
		done <- outcome{res, err}
	}()

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("migration never started")
	}
	st := progress.Snapshot()
	if !st.Migrating || st.Text != "copying row 7 of 100" {
		t.Errorf("snapshot during migration = %+v", st)
	}
	if got := st.Message(); got != "migrating: copying row 7 of 100" {
		t.Errorf("Message = %q", got)
	}
	select {
	case o := <-done:
		t.Fatalf("Open returned while the migration was blocked: %+v", o)
	default:
	}

	close(release)
	select {
	case o := <-done:
		if o.err != nil || o.res.Outcome != OutcomeMigrated {
			t.Fatalf("Open = %+v", o)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Open never returned")
	}
	if st := progress.Snapshot(); st.Migrating || st.Message() != "" {
		t.Errorf("snapshot after Open = %+v, want idle", st)
	}
}

func TestMigrationStepTextNamesTheStep(t *testing.T) {
	progress := NewProgress()
	var seen []string
	opts := fastOpts()
	opts.Progress = progress
	opts.Migrations = []Migration{
		{Version: 1, Name: "first", Apply: func(tx *sql.Tx, _ func(string)) error {
			seen = append(seen, progress.Snapshot().Text)
			return nil
		}},
		{Version: 2, Name: "second", Apply: func(tx *sql.Tx, _ func(string)) error {
			seen = append(seen, progress.Snapshot().Text)
			return nil
		}},
	}
	mustOpen(t, dbPath(t), opts)
	want := []string{"applying schema 1 of 2: first", "applying schema 2 of 2: second"}
	if !slices.Equal(seen, want) {
		t.Errorf("progress texts = %v, want %v", seen, want)
	}
}
