package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticDBName is the checked-in pre-cutover (schema version 1) store
// under testdata/. It is built by running the OLD migrations and seeding
// rows through the OLD write API (buildSyntheticPreMigrationDB); rebuild it
// with:
//
//	PG_DESK_REGEN_STORE_TESTDATA=1 go test ./internal/store/ -run TestRegenerateSyntheticPreMigrationDB
//
// Every value in it is synthetic.
const syntheticDBName = "pre-cutover-store.sqlite"

// buildSyntheticPreMigrationDB creates a schema-version-1 store at path by
// running the old migrations (Open never runs the cutover) and seeding at
// least one row in every table through the old write API, including the
// row shapes the cutover's reserved-key copy has to translate.
func buildSyntheticPreMigrationDB(t *testing.T, path string) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open %s: %v", path, err)
	}

	const repo = "acme/widgets"
	for i, e := range []Entity{
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", Facts: `{"title":"first change"}`, AsOf: "2026-09-01T00:00:00Z", ContentHash: "h1", HeadSHA: "aaaa1111"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#2", Facts: `{"title":"second change"}`, AsOf: "2026-09-02T00:00:00Z", Stale: true, ContentHash: "h2"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#3", Facts: `{"title":"third change"}`, AsOf: "2026-09-03T00:00:00Z", ContentHash: "h3"},
	} {
		if err := s.UpsertEntity(e); err != nil {
			t.Fatalf("seed entity %d: %v", i, err)
		}
	}

	for i, in := range []Interpretation{
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", Ownership: "mine", Enrichment: `{"kind":"feature"}`, Urgency: `{"level":"low"}`, Category: "feature", Dispositions: `{}`, Approvals: `[]`, GateState: "green", MatchReasons: `["authored"]`, Panel: "mine_awaiting_team", ReadyToPromote: true, AsOf: "2026-09-01T00:00:00Z"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#2", Ownership: "team", Panel: "team_awaiting_me", Degraded: true, SyncError: "bead create failed", AsOf: "2026-09-02T00:00:00Z"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#3", Ownership: "team", Panel: "team_awaiting_owner", AsOf: "2026-09-03T00:00:00Z"},
	} {
		if err := s.UpsertInterpretation(in); err != nil {
			t.Fatalf("seed interpretation %d: %v", i, err)
		}
	}

	for i, x := range []Xref{
		{Repo: repo, FromType: "pr", FromID: repo + "#1", ToType: "issue", ToID: "PROJ-1", Evidence: "branch", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-04T00:00:00Z"},
		{Repo: repo, FromType: "pr", FromID: repo + "#2", ToType: "issue", ToID: "PROJ-1", Evidence: "title", FirstSeen: "2026-09-02T00:00:00Z", LastConfirmed: "2026-09-04T00:00:00Z"},
	} {
		if err := s.UpsertXref(x); err != nil {
			t.Fatalf("seed xref %d: %v", i, err)
		}
	}

	yes, no := true, false
	for i, a := range []Annotation{
		// PR-level row carrying both a hidden state (with reason) and WIP.
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", Hidden: &yes, HiddenReason: "waiting on upstream", WIP: &yes, SetBy: "operator", SetAt: "2026-09-05T00:00:00Z"},
		// PR-level row that un-hid (hidden=0, no reason) and cleared WIP.
		{Repo: repo, EntityType: "pr", EntityID: repo + "#2", Hidden: &no, WIP: &no, SetBy: "review-bot", SetAt: "2026-09-06T00:00:00Z"},
		// PR-level row that set neither hidden nor WIP: yields no reserved key.
		{Repo: repo, EntityType: "pr", EntityID: repo + "#3", SetBy: "operator", SetAt: "2026-09-07T00:00:00Z"},
		// Per-comment disposition overrides.
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", CommentID: "c100", Disposition: "will-fix", SetBy: "operator", SetAt: "2026-09-08T00:00:00Z"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", CommentID: "c200", Disposition: "wont-fix", SetBy: "review-bot", SetAt: "2026-09-09T00:00:00Z"},
	} {
		if err := s.UpsertAnnotation(a); err != nil {
			t.Fatalf("seed annotation %d: %v", i, err)
		}
	}

	for i, l := range []LedgerEntry{
		{Repo: repo, EntityType: "pr", EntityID: repo + "#1", Kind: "anchor", BeadID: "bead-1", LastSyncedContentHash: "h1", LastSyncedAt: "2026-09-04T00:00:00Z"},
		{Repo: repo, EntityType: "pr", EntityID: repo + "#2", Kind: "review-request", BeadID: "bead-2", LastReviewedHeadSHA: "bbbb2222"},
	} {
		if err := s.UpsertLedger(l); err != nil {
			t.Fatalf("seed ledger %d: %v", i, err)
		}
	}

	if err := s.SetMeta(MetaKeyLastHeartbeat, "2026-09-10T00:00:00Z"); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Leave one self-contained file (no -wal/-shm sidecars) so the result is
	// safe to check in and to copy byte-for-byte.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen for finalize: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatalf("journal_mode=DELETE: %v", err)
	}
	if _, err := raw.Exec("VACUUM"); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}
}

// TestRegenerateSyntheticPreMigrationDB rewrites the checked-in fixture. It
// is skipped unless PG_DESK_REGEN_STORE_TESTDATA=1, so an ordinary test run
// never touches testdata/.
func TestRegenerateSyntheticPreMigrationDB(t *testing.T) {
	if os.Getenv("PG_DESK_REGEN_STORE_TESTDATA") != "1" {
		t.Skip("set PG_DESK_REGEN_STORE_TESTDATA=1 to rebuild testdata/" + syntheticDBName)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	dst := filepath.Join("testdata", syntheticDBName)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(dst + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", dst+suffix, err)
		}
	}
	buildSyntheticPreMigrationDB(t, dst)
}

// copySyntheticDB copies the checked-in fixture into a temp dir and returns
// the copy's path, so a test can migrate it without touching testdata/.
func copySyntheticDB(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", syntheticDBName))
	if err != nil {
		t.Fatalf("read checked-in synthetic DB: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	return dst
}

// dumpDB renders everything observable about a store: user_version, the
// schema (sqlite_master), and every row of every table. Two stores with
// equal dumps are functionally identical.
func dumpDB(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	fmt.Fprintf(&b, "user_version=%d\n", v)

	rows, err := s.sql.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, tbl, ddl string
		if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		fmt.Fprintf(&b, "%s %s on %s: %s\n", typ, name, tbl, ddl)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	_ = rows.Close()

	for _, table := range tables {
		cols := tableColumns(t, s, table)
		order := make([]string, len(cols))
		for i := range cols {
			order[i] = fmt.Sprint(i + 1)
		}
		rs, err := s.sql.Query(fmt.Sprintf(`SELECT * FROM %q ORDER BY %s`, table, strings.Join(order, ", ")))
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		fmt.Fprintf(&b, "-- %s\n", table)
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			for _, val := range vals {
				if bs, ok := val.([]byte); ok {
					val = string(bs)
				}
				fmt.Fprintf(&b, "%T:%v|", val, val)
			}
			b.WriteString("\n")
		}
		if err := rs.Err(); err != nil {
			t.Fatalf("iterate %s: %v", table, err)
		}
		_ = rs.Close()
	}
	return b.String()
}

// columnInfo is one row of PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull bool
	Default sql.NullString
	PK      int
}

func tableInfo(t *testing.T, s *Store, table string) []columnInfo {
	t.Helper()
	rows, err := s.sql.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []columnInfo
	for rows.Next() {
		var cid int
		var c columnInfo
		var notnull int
		if err := rows.Scan(&cid, &c.Name, &c.Type, &notnull, &c.Default, &c.PK); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		c.NotNull = notnull != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var names []string
	for _, c := range tableInfo(t, s, table) {
		names = append(names, c.Name)
	}
	return names
}

func tableExists(t *testing.T, s *Store, table string) bool {
	t.Helper()
	var n int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		t.Fatalf("tableExists(%s): %v", table, err)
	}
	return n > 0
}

func column(t *testing.T, s *Store, table, name string) (columnInfo, bool) {
	t.Helper()
	for _, c := range tableInfo(t, s, table) {
		if c.Name == name {
			return c, true
		}
	}
	return columnInfo{}, false
}

func metaSchemaVersion(t *testing.T, s *Store) string {
	t.Helper()
	v, found, err := s.GetMeta(MetaKeySchemaVersion)
	if err != nil || !found {
		t.Fatalf("meta.schema_version: found=%v err=%v", found, err)
	}
	return v
}

// openSyntheticCopy opens a fresh copy of the checked-in pre-cutover DB
// with Open (old migrations only), returning the store and its path.
func openSyntheticCopy(t *testing.T) (*Store, string) {
	t.Helper()
	path := copySyntheticDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open synthetic copy: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// TestSyntheticPreMigrationDBMatchesBuilder keeps the checked-in fixture
// honest: it must equal what the generator produces from the OLD
// migrations today, and be at schema version 1.
func TestSyntheticPreMigrationDBMatchesBuilder(t *testing.T) {
	built := filepath.Join(t.TempDir(), "built.db")
	buildSyntheticPreMigrationDB(t, built)
	sb, err := OpenRaw(built)
	if err != nil {
		t.Fatalf("OpenRaw built: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	sc, err := OpenRaw(copySyntheticDB(t))
	if err != nil {
		t.Fatalf("OpenRaw checked-in: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if v, _ := sc.SchemaVersion(); v != 1 {
		t.Fatalf("checked-in synthetic DB user_version = %d, want 1", v)
	}
	if got, want := dumpDB(t, sc), dumpDB(t, sb); got != want {
		t.Fatalf("checked-in %s is stale versus buildSyntheticPreMigrationDB; rebuild it with PG_DESK_REGEN_STORE_TESTDATA=1\n--- checked in ---\n%s\n--- built ---\n%s", syntheticDBName, got, want)
	}
}

// TestMigrationAddsEntityColumns asserts the cutover's whole structural
// effect on a fresh store: entity gains version/hydrated_at/active,
// interpretation loses sync_error, ledger is gone, and change_log and
// consumer exist exactly as designed.
func TestMigrationAddsEntityColumns(t *testing.T) {
	s := OpenNewSchemaForTest(t)

	if v, err := s.SchemaVersion(); err != nil || v != NewSchemaVersion {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, NewSchemaVersion)
	}
	if got := metaSchemaVersion(t, s); got != "2" {
		t.Fatalf("meta.schema_version = %q, want 2", got)
	}

	version, ok := column(t, s, "entity", "version")
	if !ok || version.Type != "INTEGER" || !version.NotNull || version.Default.String != "0" {
		t.Errorf("entity.version = %+v (present=%v), want INTEGER NOT NULL DEFAULT 0", version, ok)
	}
	hydrated, ok := column(t, s, "entity", "hydrated_at")
	if !ok || hydrated.Type != "TEXT" || hydrated.NotNull {
		t.Errorf("entity.hydrated_at = %+v (present=%v), want nullable TEXT", hydrated, ok)
	}
	active, ok := column(t, s, "entity", "active")
	if !ok || active.Type != "INTEGER" || !active.NotNull || active.Default.String != "1" {
		t.Errorf("entity.active = %+v (present=%v), want INTEGER NOT NULL DEFAULT 1", active, ok)
	}

	if _, ok := column(t, s, "interpretation", "sync_error"); ok {
		t.Errorf("interpretation.sync_error still present after cutover")
	}
	if tableExists(t, s, "ledger") {
		t.Errorf("ledger table still present after cutover")
	}

	wantChangeLog := []string{"seq", "repo", "entity_type", "entity_id", "version", "kinds", "origin", "at"}
	if got := tableColumns(t, s, "change_log"); strings.Join(got, ",") != strings.Join(wantChangeLog, ",") {
		t.Errorf("change_log columns = %v, want %v", got, wantChangeLog)
	}
	if seq, _ := column(t, s, "change_log", "seq"); seq.PK != 1 || seq.Type != "INTEGER" {
		t.Errorf("change_log.seq = %+v, want INTEGER primary key", seq)
	}
	for _, name := range []string{"repo", "entity_type", "entity_id", "version", "kinds", "origin", "at"} {
		if c, _ := column(t, s, "change_log", name); !c.NotNull {
			t.Errorf("change_log.%s must be NOT NULL", name)
		}
	}
	var seqSQL string
	if err := s.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'change_log'`).Scan(&seqSQL); err != nil {
		t.Fatalf("read change_log ddl: %v", err)
	}
	if !strings.Contains(strings.ToUpper(seqSQL), "AUTOINCREMENT") {
		t.Errorf("change_log.seq must be AUTOINCREMENT; ddl: %s", seqSQL)
	}
	idxCols := indexColumns(t, s, "change_log_entity")
	if strings.Join(idxCols, ",") != "repo,entity_type,entity_id,seq" {
		t.Errorf("change_log_entity columns = %v, want repo,entity_type,entity_id,seq", idxCols)
	}

	wantConsumer := []string{"name", "type", "cursor", "seen_at"}
	if got := tableColumns(t, s, "consumer"); strings.Join(got, ",") != strings.Join(wantConsumer, ",") {
		t.Errorf("consumer columns = %v, want %v", got, wantConsumer)
	}
	cursor, _ := column(t, s, "consumer", "cursor")
	if cursor.Type != "INTEGER" || !cursor.NotNull || cursor.Default.String != "0" {
		t.Errorf("consumer.cursor = %+v, want INTEGER NOT NULL DEFAULT 0", cursor)
	}
	if seen, _ := column(t, s, "consumer", "seen_at"); seen.NotNull {
		t.Errorf("consumer.seen_at must be nullable")
	}
	if name, _ := column(t, s, "consumer", "name"); name.PK != 1 {
		t.Errorf("consumer.name pk position = %d, want 1", name.PK)
	}
	if typ, _ := column(t, s, "consumer", "type"); typ.PK != 2 {
		t.Errorf("consumer.type pk position = %d, want 2", typ.PK)
	}

	wantAnnotation := []string{"repo", "entity_type", "entity_id", "key", "value", "origin", "set_by", "set_at"}
	if got := tableColumns(t, s, "annotation"); strings.Join(got, ",") != strings.Join(wantAnnotation, ",") {
		t.Errorf("annotation columns = %v, want %v", got, wantAnnotation)
	}
	for i, name := range []string{"repo", "entity_type", "entity_id", "key"} {
		if c, _ := column(t, s, "annotation", name); c.PK != i+1 {
			t.Errorf("annotation.%s pk position = %d, want %d", name, c.PK, i+1)
		}
	}
	if tableExists(t, s, "annotation_v2") {
		t.Errorf("annotation_v2 must have been renamed to annotation")
	}
}

// indexColumns returns an index's columns in index order.
func indexColumns(t *testing.T, s *Store, index string) []string {
	t.Helper()
	rows, err := s.sql.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, index))
	if err != nil {
		t.Fatalf("index_info(%s): %v", index, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info(%s): %v", index, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index_info(%s): %v", index, err)
	}
	return out
}

// TestMigrateFromSyntheticPreMigrationDB migrates the checked-in database
// (built by the OLD migrations) and asserts the pre-existing rows survive
// with the schema-2 shape.
func TestMigrateFromSyntheticPreMigrationDB(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if v, _ := s.SchemaVersion(); v != 1 {
		t.Fatalf("precondition: user_version = %d, want 1", v)
	}
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}

	if v, _ := s.SchemaVersion(); v != 2 {
		t.Fatalf("user_version = %d, want 2", v)
	}
	if got := metaSchemaVersion(t, s); got != "2" {
		t.Fatalf("meta.schema_version = %q, want 2", got)
	}
	// Non-schema meta rows are untouched.
	if hb, found, _ := s.GetMeta(MetaKeyLastHeartbeat); !found || hb != "2026-09-10T00:00:00Z" {
		t.Fatalf("meta.last_heartbeat = %q found=%v, want the seeded value", hb, found)
	}

	// entity rows: content intact; new columns take their defaults.
	entities, err := s.ListEntities()
	if err != nil || len(entities) != 3 {
		t.Fatalf("ListEntities = %d rows, err=%v; want 3", len(entities), err)
	}
	if entities[0].Facts != `{"title":"first change"}` || entities[0].HeadSHA != "aaaa1111" || !entities[1].Stale {
		t.Errorf("entity content changed by cutover: %+v", entities)
	}
	var version, active int
	var hydrated sql.NullString
	if err := s.sql.QueryRow(`SELECT version, hydrated_at, active FROM entity WHERE entity_id = 'acme/widgets#1'`).Scan(&version, &hydrated, &active); err != nil {
		t.Fatalf("read new entity columns: %v", err)
	}
	if version != 0 || hydrated.Valid || active != 1 {
		t.Errorf("new entity columns = (%d, %v, %d), want (0, NULL, 1)", version, hydrated, active)
	}

	// interpretation rows survive, sync_error is gone.
	interps, err := s.ListInterpretations()
	if err != nil || len(interps) != 3 {
		t.Fatalf("ListInterpretations = %d rows, err=%v; want 3", len(interps), err)
	}
	if interps[0].Ownership != "mine" || !interps[0].ReadyToPromote || interps[0].Panel != "mine_awaiting_team" || !interps[1].Degraded {
		t.Errorf("interpretation content changed by cutover: %+v", interps)
	}
	if interps[1].SyncError != "" {
		t.Errorf("SyncError = %q on a schema-2 store, want empty", interps[1].SyncError)
	}

	// ledger is dropped; the new tables exist and start empty.
	if tableExists(t, s, "ledger") {
		t.Errorf("ledger still exists")
	}
	for _, table := range []string{"change_log", "consumer"} {
		var n int
		if err := s.sql.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, table)).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows = %d, err=%v; want an empty table", table, n, err)
		}
	}

	// xref: rows migrated and readable through the old xref API.
	xrefs, err := s.ListXrefsByTo("acme/widgets", "issue", "PROJ-1")
	if err != nil || len(xrefs) != 2 {
		t.Fatalf("ListXrefsByTo = %d rows, err=%v; want 2", len(xrefs), err)
	}
}

// TestAnnotationReservedKeyMigration asserts the annotation copy: hidden,
// wip and per-comment disposition rows land on their reserved keys with
// origin pg-desk and set_by/set_at preserved, and rows carrying no such
// state produce no key.
func TestAnnotationReservedKeyMigration(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}

	type row struct{ key, value, origin, setBy, setAt string }
	want := map[string]row{
		"acme/widgets#1/hidden":           {"hidden", `{"value":true,"reason":"waiting on upstream"}`, "pg-desk", "operator", "2026-09-05T00:00:00Z"},
		"acme/widgets#1/wip":              {"wip", "true", "pg-desk", "operator", "2026-09-05T00:00:00Z"},
		"acme/widgets#1/disposition.c100": {"disposition.c100", "will-fix", "pg-desk", "operator", "2026-09-08T00:00:00Z"},
		"acme/widgets#1/disposition.c200": {"disposition.c200", "wont-fix", "pg-desk", "review-bot", "2026-09-09T00:00:00Z"},
		"acme/widgets#2/hidden":           {"hidden", `{"value":false,"reason":null}`, "pg-desk", "review-bot", "2026-09-06T00:00:00Z"},
		"acme/widgets#2/wip":              {"wip", "false", "pg-desk", "review-bot", "2026-09-06T00:00:00Z"},
	}

	rows, err := s.sql.Query(`SELECT repo, entity_type, entity_id, key, value, origin, set_by, set_at FROM annotation`)
	if err != nil {
		t.Fatalf("read annotation: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]row{}
	for rows.Next() {
		var repo, typ, id string
		var r row
		if err := rows.Scan(&repo, &typ, &id, &r.key, &r.value, &r.origin, &r.setBy, &r.setAt); err != nil {
			t.Fatalf("scan annotation: %v", err)
		}
		if repo != "acme/widgets" || typ != "pr" {
			t.Errorf("annotation row keyed (%s,%s), want (acme/widgets,pr)", repo, typ)
		}
		got[id+"/"+r.key] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate annotation: %v", err)
	}
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("missing migrated annotation %s", k)
		} else if g != w {
			t.Errorf("annotation %s = %+v, want %+v", k, g, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected migrated annotation %s (a row with no hidden/wip/disposition state must produce no key)", k)
		}
	}
}

// TestCutoverMigratesXrefToOriginRelation covers the xref rebuild: legacy
// rows become origin='derived:legacy', relation='references', the new
// columns exist, and the primary key is (repo, from_type, from_id, to_type,
// to_id, relation, origin).
func TestCutoverMigratesXrefToOriginRelation(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}

	wantCols := []string{"repo", "from_type", "from_id", "to_type", "to_id", "origin", "relation", "evidence", "first_seen", "last_confirmed", "actor", "acted_at", "reason"}
	if got := tableColumns(t, s, "xref"); strings.Join(got, ",") != strings.Join(wantCols, ",") {
		t.Fatalf("xref columns = %v, want %v", got, wantCols)
	}
	for _, name := range []string{"origin", "relation", "first_seen", "last_confirmed"} {
		if c, _ := column(t, s, "xref", name); !c.NotNull {
			t.Errorf("xref.%s must be NOT NULL", name)
		}
	}
	for _, name := range []string{"actor", "acted_at", "reason"} {
		if c, _ := column(t, s, "xref", name); c.NotNull || c.Type != "TEXT" {
			t.Errorf("xref.%s = %+v, want nullable TEXT", name, c)
		}
	}
	wantPK := []string{"repo", "from_type", "from_id", "to_type", "to_id", "relation", "origin"}
	pkByPos := map[int]string{}
	for _, c := range tableInfo(t, s, "xref") {
		if c.PK > 0 {
			pkByPos[c.PK] = c.Name
		}
	}
	var gotPK []string
	for i := 1; i <= len(pkByPos); i++ {
		gotPK = append(gotPK, pkByPos[i])
	}
	if strings.Join(gotPK, ",") != strings.Join(wantPK, ",") {
		t.Errorf("xref primary key = %v, want %v", gotPK, wantPK)
	}

	var origin, relation, evidence, first, last string
	var actor, actedAt, reason sql.NullString
	if err := s.sql.QueryRow(`SELECT origin, relation, evidence, first_seen, last_confirmed, actor, acted_at, reason FROM xref WHERE from_id = 'acme/widgets#1'`).
		Scan(&origin, &relation, &evidence, &first, &last, &actor, &actedAt, &reason); err != nil {
		t.Fatalf("read migrated xref: %v", err)
	}
	if origin != "derived:legacy" || relation != "references" || evidence != "branch" ||
		first != "2026-09-01T00:00:00Z" || last != "2026-09-04T00:00:00Z" || actor.Valid || actedAt.Valid || reason.Valid {
		t.Errorf("migrated xref = (%s,%s,%s,%s,%s,%v,%v,%v)", origin, relation, evidence, first, last, actor, actedAt, reason)
	}

	// The widened key admits the same endpoints under another relation/origin
	// and still rejects an exact duplicate.
	insert := func(relation, origin string) error {
		_, err := s.sql.Exec(`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, first_seen, last_confirmed)
			VALUES ('acme/widgets', 'pr', 'acme/widgets#1', 'issue', 'PROJ-1', ?, ?, 't', 't')`, origin, relation)
		return err
	}
	if err := insert("closes", "derived:legacy"); err != nil {
		t.Errorf("same endpoints, different relation must be allowed: %v", err)
	}
	if err := insert("references", "external:teammate"); err != nil {
		t.Errorf("same endpoints, different origin must be allowed: %v", err)
	}
	if err := insert("references", "derived:legacy"); err == nil {
		t.Errorf("duplicate (endpoints, relation, origin) must violate the primary key")
	}
}

// TestMigrateIsIdempotentOnMigratedStore re-runs the cutover on a migrated
// store (no-op, no error, nothing changes) and re-opens it with Open, which
// must accept version 2 and MUST NOT rewrite meta.schema_version.
func TestMigrateIsIdempotentOnMigratedStore(t *testing.T) {
	s, path := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("first Cutover: %v", err)
	}
	before := dumpDB(t, s)

	if err := s.Cutover(); err != nil {
		t.Fatalf("second Cutover (must be a no-op): %v", err)
	}
	if after := dumpDB(t, s); after != before {
		t.Fatalf("second Cutover changed the store:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version-2 store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := metaSchemaVersion(t, reopened); got != "2" {
		t.Fatalf("meta.schema_version = %q after Open on a migrated store, want it to stay \"2\"", got)
	}
	if after := dumpDB(t, reopened); after != before {
		t.Fatalf("Open changed a version-2 store:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if err := reopened.Cutover(); err != nil {
		t.Fatalf("Cutover after reopening: %v", err)
	}
}

// TestMigrateFailureLeavesStoreUnchanged injects a failing statement at
// EVERY position of the cutover block (including the final version-stamp
// steps and after the last statement) and asserts the store is
// functionally unchanged: same schema, same rows, same user_version.
func TestMigrateFailureLeavesStoreUnchanged(t *testing.T) {
	baseline, _ := openSyntheticCopy(t)
	want := dumpDB(t, baseline)
	if len(cutoverStatements) < 10 {
		t.Fatalf("cutoverStatements has only %d steps; the block is missing", len(cutoverStatements))
	}

	for pos := 0; pos <= len(cutoverStatements); pos++ {
		t.Run(fmt.Sprintf("fail_before_step_%02d", pos), func(t *testing.T) {
			s, _ := openSyntheticCopy(t)
			broken := append([]string(nil), cutoverStatements[:pos]...)
			broken = append(broken, "INSERT INTO table_that_does_not_exist VALUES (1)")
			broken = append(broken, cutoverStatements[pos:]...)

			err := s.cutoverWith(broken)
			if err == nil {
				t.Fatalf("cutoverWith succeeded despite an injected failure at step %d", pos)
			}
			if got := dumpDB(t, s); got != want {
				t.Fatalf("store changed after a failed cutover at step %d:\n--- want ---\n%s\n--- got ---\n%s", pos, want, got)
			}
			// The store must still be a working schema-1 store, and a
			// clean cutover must still succeed afterwards.
			if err := s.Cutover(); err != nil {
				t.Fatalf("Cutover after a rolled-back attempt: %v", err)
			}
			if v, _ := s.SchemaVersion(); v != 2 {
				t.Fatalf("user_version = %d after the retry, want 2", v)
			}
		})
	}
}

// TestCutoverRefusesUnexpectedVersion covers the two states Cutover must
// not touch: an uninitialized file (version 0) and a version newer than
// this binary knows.
func TestCutoverRefusesUnexpectedVersion(t *testing.T) {
	empty, err := OpenRaw(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = empty.Close() })
	if err := empty.Cutover(); err == nil {
		t.Errorf("Cutover on an uninitialized store must fail")
	}
	if tableExists(t, empty, "entity") {
		t.Errorf("failed Cutover created tables on an uninitialized store")
	}

	future, err := OpenRaw(filepath.Join(t.TempDir(), "future.db"))
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = future.Close() })
	if _, err := future.sql.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := future.Cutover(); err == nil {
		t.Errorf("Cutover on a version-3 store must fail")
	}
}

// TestOpenLeavesOldSchemaAlone proves Open never runs the cutover: a
// schema-1 store stays schema 1 (old annotation shape, no change_log, meta
// still "1") across repeated Opens.
func TestOpenLeavesOldSchemaAlone(t *testing.T) {
	_, path := openSyntheticCopy(t)
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if v, _ := s.SchemaVersion(); v != 1 {
			t.Errorf("Open #%d: user_version = %d, want 1", i, v)
		}
		if got := metaSchemaVersion(t, s); got != "1" {
			t.Errorf("Open #%d: meta.schema_version = %q, want 1", i, got)
		}
		if tableExists(t, s, "change_log") || tableExists(t, s, "consumer") {
			t.Errorf("Open #%d created cutover tables", i)
		}
		if _, ok := column(t, s, "annotation", "comment_id"); !ok {
			t.Errorf("Open #%d: annotation lost its old shape", i)
		}
		if !tableExists(t, s, "ledger") {
			t.Errorf("Open #%d: ledger table missing", i)
		}
		_ = s.Close()
	}
}

// TestOpenRefusesNewerThanBinaryVersion keeps Open's forward gate: a store
// newer than the cutover version is refused.
func TestOpenRefusesNewerThanBinaryVersion(t *testing.T) {
	raw, err := OpenRaw(filepath.Join(t.TempDir(), "future.db"))
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if _, err := raw.sql.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	path := raw.path
	_ = raw.Close()

	s, err := Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatalf("Open on a version-3 store must fail")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error = %v, want it to say the store is newer than this binary supports", err)
	}
}

// preCutoverMigrate is a verbatim copy of migrate() as it stood BEFORE the
// cutover work: it treats any user_version above the migration count as
// "newer than this binary supports". It stands in for the OLD binary a
// rollback would run, which is what makes rolling back after the cutover
// loud rather than silently corrupting. Do not "fix" it to track migrate().
func preCutoverMigrate(s *Store) error {
	var current int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, len(migrations))
	}
	for i := current; i < len(migrations); i++ {
		tx, err := s.sql.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	if _, err := s.sql.Exec(
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%d", len(migrations)),
	); err != nil {
		return fmt.Errorf("mirror schema_version into meta: %w", err)
	}
	return nil
}

// TestPreCutoverBinaryRefusesVersion2Store simulates the old binary opening
// a migrated store and asserts it refuses loudly, leaving the store
// untouched; the same helper accepts a schema-1 store, so the refusal is
// not vacuous.
func TestPreCutoverBinaryRefusesVersion2Store(t *testing.T) {
	old, _ := openSyntheticCopy(t)
	if err := preCutoverMigrate(old); err != nil {
		t.Fatalf("the pre-cutover gate must accept a schema-1 store: %v", err)
	}

	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	before := dumpDB(t, s)
	err := preCutoverMigrate(s)
	if err == nil {
		t.Fatalf("the pre-cutover gate accepted a version-2 store")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error = %v, want the loud newer-than-binary refusal", err)
	}
	if after := dumpDB(t, s); after != before {
		t.Errorf("the refused open modified the store")
	}
}

// TestSchemaCheckRefusesOldSchemaStore covers SchemaVersion and the
// RequireNewSchema helper every new-schema command calls.
func TestSchemaCheckRefusesOldSchemaStore(t *testing.T) {
	const want = "run pg-desk migrate --cutover"

	old := OpenForTest(t)
	if v, err := old.SchemaVersion(); err != nil || v != 1 {
		t.Fatalf("old SchemaVersion = %d, %v; want 1", v, err)
	}
	err := old.RequireNewSchema()
	if err == nil {
		t.Fatalf("RequireNewSchema accepted an old-schema store")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
	if !errors.Is(err, ErrOldSchema) {
		t.Errorf("error = %v, want errors.Is(err, ErrOldSchema)", err)
	}

	fresh := OpenNewSchemaForTest(t)
	if v, err := fresh.SchemaVersion(); err != nil || v != 2 {
		t.Fatalf("new SchemaVersion = %d, %v; want 2", v, err)
	}
	if err := fresh.RequireNewSchema(); err != nil {
		t.Errorf("RequireNewSchema refused a version-2 store: %v", err)
	}

	// An uninitialized file (OpenRaw runs no migrations) is also "not new".
	empty, err2 := OpenRaw(filepath.Join(t.TempDir(), "empty.db"))
	if err2 != nil {
		t.Fatalf("OpenRaw: %v", err2)
	}
	t.Cleanup(func() { _ = empty.Close() })
	if v, err := empty.SchemaVersion(); err != nil || v != 0 {
		t.Fatalf("empty SchemaVersion = %d, %v; want 0", v, err)
	}
	if err := empty.RequireNewSchema(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("RequireNewSchema on an uninitialized store = %v, want an error containing %q", err, want)
	}
}

// TestOpenRawSkipsTheVersionGate proves OpenRaw runs no migrations and no
// gate: it opens a store Open would refuse (version 3) and a file with no
// schema at all, and creates nothing.
func TestOpenRawSkipsTheVersionGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.db")
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw on a new path: %v", err)
	}
	if tableExists(t, raw, "entity") || tableExists(t, raw, "meta") {
		t.Errorf("OpenRaw ran migrations")
	}
	if _, err := raw.sql.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	_ = raw.Close()

	again, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw on a version-3 store: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if v, _ := again.SchemaVersion(); v != 3 {
		t.Errorf("SchemaVersion = %d, want 3", v)
	}
}

// TestEntityAndInterpretationAccessWorkOnBothSchemas runs the existing
// entity, interpretation and xref read/write API against a schema-1 and a
// schema-2 store: the same calls must behave the same on both.
func TestEntityAndInterpretationAccessWorkOnBothSchemas(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) *Store
		v2   bool
	}{
		{"schema1", func(t *testing.T) *Store { return OpenForTest(t) }, false},
		{"schema2", func(t *testing.T) *Store { return OpenNewSchemaForTest(t) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.open(t)

			// entity
			e := Entity{Repo: "acme/widgets", EntityType: "pr", EntityID: "acme/widgets#7", Facts: `{"title":"x"}`, AsOf: "2026-09-16T00:00:00Z", ContentHash: "c1", HeadSHA: "deadbeef"}
			if err := s.UpsertEntity(e); err != nil {
				t.Fatalf("UpsertEntity: %v", err)
			}
			got, found, err := s.GetEntity(e.Repo, e.EntityType, e.EntityID)
			if err != nil || !found || got != e {
				t.Fatalf("GetEntity = %+v, found=%v, err=%v; want %+v", got, found, err, e)
			}
			e.Facts, e.Stale = `{"title":"y"}`, true
			if err := s.UpsertEntity(e); err != nil {
				t.Fatalf("UpsertEntity (update): %v", err)
			}
			if got, _, _ := s.GetEntity(e.Repo, e.EntityType, e.EntityID); got != e {
				t.Fatalf("GetEntity after update = %+v, want %+v", got, e)
			}
			if n, err := s.CountEntities(); err != nil || n != 1 {
				t.Fatalf("CountEntities = %d, %v; want 1", n, err)
			}
			if list, err := s.ListEntities(); err != nil || len(list) != 1 || list[0] != e {
				t.Fatalf("ListEntities = %+v, %v; want [%+v]", list, err, e)
			}
			if tc.v2 {
				// The new-schema columns are untouched by the old writer.
				var version, active int
				var hydrated sql.NullString
				if err := s.sql.QueryRow(`SELECT version, hydrated_at, active FROM entity`).Scan(&version, &hydrated, &active); err != nil {
					t.Fatalf("read new columns: %v", err)
				}
				if version != 0 || hydrated.Valid || active != 1 {
					t.Errorf("new columns = (%d, %v, %d), want defaults (0, NULL, 1)", version, hydrated, active)
				}
			}

			// interpretation
			in := Interpretation{
				Repo: "acme/widgets", EntityType: "pr", EntityID: "acme/widgets#7",
				Ownership: "mine", Enrichment: `{"k":1}`, Urgency: `{"u":1}`, Category: "feature",
				Dispositions: `{}`, Approvals: `[]`, GateState: "green", MatchReasons: `["a"]`,
				Panel: "mine_awaiting_team", ReadyToPromote: true, Degraded: true, AsOf: "2026-09-16T00:00:00Z",
			}
			if !tc.v2 {
				in.SyncError = "sync failed"
			}
			if err := s.UpsertInterpretation(in); err != nil {
				t.Fatalf("UpsertInterpretation: %v", err)
			}
			gi, found, err := s.GetInterpretation(in.Repo, in.EntityType, in.EntityID)
			if err != nil || !found || gi != in {
				t.Fatalf("GetInterpretation = %+v, found=%v, err=%v; want %+v", gi, found, err, in)
			}
			in.Panel = "team_awaiting_me"
			if err := s.UpsertInterpretation(in); err != nil {
				t.Fatalf("UpsertInterpretation (update): %v", err)
			}
			list, err := s.ListInterpretations()
			if err != nil || len(list) != 1 || list[0] != in {
				t.Fatalf("ListInterpretations = %+v, %v; want [%+v]", list, err, in)
			}
			if has, err := s.HasAnyInterpretation(); err != nil || !has {
				t.Fatalf("HasAnyInterpretation = %v, %v; want true", has, err)
			}

			if tc.v2 {
				// sync_error no longer exists: silently dropping a write would
				// hide a failure, so the writer says so.
				in.SyncError = "boom"
				if err := s.UpsertInterpretation(in); err == nil {
					t.Errorf("UpsertInterpretation with SyncError on a schema-2 store must fail")
				}
			}

			// xref (old API)
			x := Xref{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "issue", ToID: "PROJ-1", Evidence: "branch", FirstSeen: "2026-09-17T00:00:00Z", LastConfirmed: "2026-09-17T00:00:00Z"}
			if err := s.UpsertXref(x); err != nil {
				t.Fatalf("UpsertXref: %v", err)
			}
			x.Evidence, x.FirstSeen, x.LastConfirmed = "title", "2026-09-30T00:00:00Z", "2026-09-18T00:00:00Z"
			if err := s.UpsertXref(x); err != nil {
				t.Fatalf("UpsertXref (update): %v", err)
			}
			gx, found, err := s.GetXref(x.Repo, x.FromType, x.FromID, x.ToType, x.ToID)
			if err != nil || !found {
				t.Fatalf("GetXref found=%v err=%v", found, err)
			}
			if gx.Evidence != "title" || gx.FirstSeen != "2026-09-17T00:00:00Z" || gx.LastConfirmed != "2026-09-18T00:00:00Z" {
				t.Errorf("GetXref after update = %+v (first_seen must survive, evidence/last_confirmed must update)", gx)
			}
			if _, found, err := s.GetXref(x.Repo, x.FromType, x.FromID, x.ToType, "PROJ-404"); err != nil || found {
				t.Errorf("GetXref of a missing row: found=%v err=%v", found, err)
			}
			byTo, err := s.ListXrefsByTo(x.Repo, "issue", "PROJ-1")
			if err != nil || len(byTo) != 1 || byTo[0].FromID != x.FromID {
				t.Errorf("ListXrefsByTo = %+v, %v", byTo, err)
			}
			byFrom, err := s.ListXrefsByFrom(x.Repo, "pr", x.FromID, "issue")
			if err != nil || len(byFrom) != 1 || byFrom[0].ToID != "PROJ-1" {
				t.Errorf("ListXrefsByFrom = %+v, %v", byFrom, err)
			}

			if tc.v2 {
				// The old API reads and writes only the legacy-marked rows.
				var origin, relation string
				if err := s.sql.QueryRow(`SELECT origin, relation FROM xref`).Scan(&origin, &relation); err != nil {
					t.Fatalf("read xref markers: %v", err)
				}
				if origin != "derived:legacy" || relation != "references" {
					t.Errorf("xref written by the old API = (%s,%s), want (derived:legacy,references)", origin, relation)
				}
				if _, err := s.sql.Exec(`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, first_seen, last_confirmed)
					VALUES ('acme/widgets', 'pr', 'acme/widgets#7', 'issue', 'PROJ-1', 'external:teammate', 'blocks', 't', 't')`); err != nil {
					t.Fatalf("insert non-legacy xref: %v", err)
				}
				if byTo, _ := s.ListXrefsByTo(x.Repo, "issue", "PROJ-1"); len(byTo) != 1 {
					t.Errorf("ListXrefsByTo returned %d rows; non-legacy rows must be invisible to the old API", len(byTo))
				}
				if byFrom, _ := s.ListXrefsByFrom(x.Repo, "pr", x.FromID, "issue"); len(byFrom) != 1 {
					t.Errorf("ListXrefsByFrom returned %d rows; non-legacy rows must be invisible to the old API", len(byFrom))
				}
			}
		})
	}
}

// TestSchemaDualAccessSurvivesCutoverOnLiveStore covers a store that was
// opened on schema 1 and then cut over under the same handle: the very next
// old-API call must already target the new shape (nothing is cached at
// Open).
func TestSchemaDualAccessSurvivesCutoverOnLiveStore(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if _, found, err := s.GetInterpretation("acme/widgets", "pr", "acme/widgets#2"); err != nil || !found {
		t.Fatalf("pre-cutover GetInterpretation found=%v err=%v", found, err)
	}
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	in, found, err := s.GetInterpretation("acme/widgets", "pr", "acme/widgets#2")
	if err != nil || !found {
		t.Fatalf("post-cutover GetInterpretation found=%v err=%v", found, err)
	}
	if in.SyncError != "" || !in.Degraded {
		t.Errorf("post-cutover interpretation = %+v", in)
	}
	in.Panel = "mine_awaiting_me"
	if err := s.UpsertInterpretation(in); err != nil {
		t.Fatalf("post-cutover UpsertInterpretation: %v", err)
	}
}
