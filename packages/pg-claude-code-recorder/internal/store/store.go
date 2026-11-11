// Package store is the recorder's durable core: the SQLite index (index.db in
// WAL mode with a schema_version), the fsynced observations journal and the
// state directory layout (copies, tail sidecars, archives).
//
// Observations (file ids, generations, path history, deletions, seen-times) are
// written to the journal first, with a sequence number, and fsynced; the
// database then records the last sequence applied. Replay is idempotent, so a
// lost or corrupt index.db is rebuilt from the journal, and a journal record
// the database never saw (a crash between the two) is applied on the next open.
//
// Durability note. index.db runs at synchronous=NORMAL in WAL mode: it never
// corrupts, but a power loss or OS crash may roll back the last commits (a
// process crash loses nothing; the WAL survives). That is acceptable because
// every table is re-derivable. The capture-side tables (files, paths,
// generations, the copy offset, last_seq, the epoch) are
// written from journal records that are fsynced BEFORE the database commit, and
// Open replays every journal record past last_seq idempotently, so a rolled
// back capture write is re-applied from the journal. The derived tables
// (records, fields, texts, parse offsets, parse_seq) commit with their parse
// offset in one transaction and roll back together; the parser resumes from the
// stored offset. A commit a delete depends on (CommitFull) and the removal
// Barrier are synchronous=FULL with F_FULLFSYNC.
//
// The copier's appends use plain fsync, which on darwin does not flush the
// drive's cache. They are therefore weaker than the settle and removal path,
// which flushes with F_FULLFSYNC before anything is deleted. A power loss can
// leave a copy shorter than its committed offset; Recover detects that and marks
// the file copy_mismatch.
//
// Full file contents are never stored in the database.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"

	_ "modernc.org/sqlite" // the database driver
)

// Options configures Open.
type Options struct {
	FS  fsx.FS   // state-directory file system (OS when nil)
	Obs *obs.Obs // events and metrics (discarding when nil)
	// StepClock is the clock of the parse step timers: a monotonic duration
	// (a real monotonic clock when nil). Tests drive it by hand.
	StepClock func() time.Duration
	// ReplayOnly opens for comparison: it never appends an epoch record.
	ReplayOnly bool
	// decoded, when set, is the journal in dir already parsed (a replay of bytes the
	// caller decoded); Open then does not read it again.
	decoded *decodedJournal
	// Rebuild forces a rebuild of index.db from the journal: the existing
	// database is set aside (index.db.pre-rebuild) and the feed epoch bumps.
	// It is also the way past a database whose schema_version is newer than
	// this binary knows.
	Rebuild bool
	// BulkRebuild (with Rebuild) opens the new index in bulk rebuild mode (see
	// bulk.go): batched commits, relaxed durability, and an unfinished-rebuild
	// marker that FinishRebuild clears. The caller MUST call FinishRebuild
	// after a successful parse.
	BulkRebuild bool
	// BulkRecords and BulkBytes override the batch commit budgets
	// (DefaultBulkRecords and DefaultBulkBytes when zero); tests use small ones.
	BulkRecords int
	BulkBytes   int64
	// DriverName is the database/sql driver name for both handles; empty
	// means "sqlite". A test registers a counting wrapper under another name
	// (internal/store/countdriver) to count reads.
	DriverName string
	// JournalSizeLimit overrides the journal_size_limit pragma in bytes
	// (JournalSizeLimit when zero); tests use a small value.
	JournalSizeLimit int
	// ReadTimeout is the deadline of every read-pool statement and read
	// transaction (DefaultReadTimeout when zero): no reader pins a WAL
	// snapshot for longer.
	ReadTimeout time.Duration
	// stmtCtx is a test seam: see deadlineConnector.stmtCtx.
	stmtCtx stmtCtxFunc
	// Phase, when set, is called as a startup phase begins (name is a stable
	// phase name) and its result is called when the phase ends; nil is a no-op.
	Phase func(name string) (done func())
	// ReadCacheKiB is the page cache of each read connection in KiB
	// (DefaultReadCacheKiB when zero). SQLite allocates it lazily, so it is a
	// ceiling per connection, not a reservation.
	ReadCacheKiB int
	// ReadMmapBytes is the memory-map ceiling of each read connection in bytes
	// (DefaultReadMmapBytes when zero, negative disables mapping). Mapped pages
	// are shared, evictable file cache, not private memory.
	ReadMmapBytes int64
	// ReadMaxOpenConns is the most connections the read pool opens at once
	// (DefaultReadMaxOpenConns when zero, negative means unbounded). Excess
	// readers wait on the pool until a slot frees or their context ends.
	ReadMaxOpenConns int
	// NoTrigramIndex builds a NEW index.db without the trigram table fts_tri:
	// the parser then indexes opaque fields (tool bodies) in the word index
	// fts_word. An existing index.db that already has fts_tri keeps it and its
	// routing, so the running schema is unchanged; the option only picks the
	// layout of a file that has none.
	NoTrigramIndex bool
	// ExtensionTables builds a NEW index.db with the extension tables (ext_*,
	// see ext.go), into which the parser routes the fields a source declares
	// with an ext: storage. An existing index.db that already has them keeps
	// them for good (a reopen without the option keeps them); one that has none
	// keeps the running layout unless the option is set.
	ExtensionTables bool
	// V3Layout builds a NEW index.db in the schema version 3 layout: the
	// extension tables, no trigram table and no records.session column (the
	// session is read through records.session_ref). The daemon always sets it.
	// An existing index.db keeps the layout it was built with, so the option
	// only picks the layout of a file that has none; a store opened without it
	// builds the previous (version 2) layout, which only the tests and the
	// parity check use.
	V3Layout bool
	// FreeBytes reports the free bytes of the file system holding a directory
	// (the file system's own figure when nil); the rebuild preflight reads it,
	// and a test supplies a fixed answer.
	FreeBytes func(dir string) (int64, error)
}

// SchemaNewerError is returned by Open when index.db carries a newer
// schema_version than this binary knows.
type SchemaNewerError struct{ Found, Known int }

func (e *SchemaNewerError) Error() string {
	return fmt.Sprintf("index.db schema_version %d is newer than the %d this binary knows; "+
		"upgrade the binary, or start with --rebuild to rebuild the index from the journal", e.Found, e.Known)
}

// SchemaOlderError is returned by Open when index.db carries an older
// schema_version than this binary knows. The derived part is not migrated in
// place; it is rebuilt from the journal and the copies.
type SchemaOlderError struct{ Found, Known int }

func (e *SchemaOlderError) Error() string {
	return fmt.Sprintf("index.db schema_version %d is older than the %d this binary needs; "+
		"rebuild the index (the rebuild command with the daemon stopped, or serve --rebuild): "+
		"the old file is kept as index.db.pre-rebuild", e.Found, e.Known)
}

// Store is an open state directory.
type Store struct {
	dirLock      *os.File // flock on the state directory, held until Close
	driverName   string   // database/sql driver name (Options.DriverName)
	journalLimit int      // journal_size_limit in bytes
	readTO       time.Duration
	stmtCtx      stmtCtxFunc
	readCacheKiB int   // cache_size of each read connection, KiB
	readMmap     int64 // mmap_size ceiling of each read connection, bytes
	readMaxOpen  int   // read pool open-connection bound (0 default, negative unbounded)
	dir          string
	fs           fsx.FS
	obs          *obs.Obs
	// trigram is whether this index.db has the trigram table fts_tri (set by
	// openDB); without it opaque fields are indexed in fts_word.
	trigram   bool
	noTrigram bool // Options.NoTrigramIndex
	// ext is whether this index.db has the extension tables (set by openDB).
	ext     bool
	wantExt bool // Options.ExtensionTables
	// v3 is Options.V3Layout; sessText is whether records has the legacy session
	// column (set by openDB).
	v3       bool
	sessText bool
	freeFn   func(string) (int64, error)
	// prof holds the phase timings of a bulk rebuild's finish (see rebuildprofile.go).
	prof RebuildProfile
	// steps and stepClock are the parse step statistics (stepstats.go).
	steps     stepState
	stepClock func() time.Duration

	Reader // the read methods over the read pool (ReadTx hands out one over a transaction)

	wdb *sql.DB // the one serialized write connection
	rdb *sql.DB
	cdb *sql.DB // the passive-checkpoint connection (own handle, never the write one)

	// Bulk rebuild state (bulk.go), guarded by mu.
	bulk        bool // opened for a bulk rebuild, not yet finished
	deferIdx    bool // bulk rebuild whose secondary indexes are not built yet
	sessionRef  bool // every records row carries a session_ref (metaSessionRef)
	batching    bool // WithWrite calls join one open transaction
	btx         *sql.Tx
	bRecs       int
	bBytes      int64
	bCommits    int
	bTotalRecs  int64 // records counted into the bulk batch since it began
	bulkRecords int
	bulkBytes   int64

	mu sync.Mutex // serializes every write
	// waitNow is the clock of the write-lock wait (time.Now when nil) and
	// onContended a test seam that runs after a lock attempt found the lock busy
	// and before it queues.
	waitNow     func() time.Time
	onContended func()
	rbEpoch     atomic.Uint64
	commitSeq   atomic.Uint64 // journal applies committed (see CommitSeq)
	pathSeq     atomic.Uint64 // journal applies that may have changed the path rows (see PathSeq)
	liveSeq     atomic.Uint64 // commits that may have changed the live-Process inputs (see LiveInputSeq)
	livePend    atomic.Bool   // a write in flight stored a live-Process input (see NoteLiveInput)
	modelSeq    atomic.Uint64 // commits that may have changed the lifecycle model inputs (see ModelInputSeq)
	modelPend   atomic.Bool   // a write in flight stored a lifecycle model input (see NoteModelInput)
	removeSeq   atomic.Uint64 // commits that removed record rows (see RemovalSeq)
	removePend  atomic.Bool   // a write in flight removed record rows (see NoteRowsRemoved)
	jf          fsx.File
	nextSeq     int64
	nextFile    int64
	lastSeq     int64
	hooks       []func()
	wrHooks     []func()
	offHooks    []OffsetsHook
	fileHooks   []FileStateHook
	pendMu      sync.Mutex // guards pendOff (a parse transaction may note from workers)
	pendOff     []offNote  // offset changes of an open write, fired after its commit

	trkMu    sync.Mutex // guards trackers
	trackers []*FileTracker

	// TornBytes is the size of the torn journal tail dropped on open.
	TornBytes int64
	// ReplayCollisions lists the file ids whose journal holds file records of
	// different mode or source (tolerated: the first one wins, as it always did).
	ReplayCollisions []int64
	// Rebuilt is true when index.db was rebuilt from the journal on open.
	Rebuilt bool
}

// Layout names inside the state directory.
const (
	DBName      = "index.db"
	JournalName = "observations.journal"
	CopiesDir   = "copies"
	ArchivesDir = "archives"
)

// Open opens (creating or recovering) the state directory dir.
//
// Open holds an exclusive flock on the state directory until Close, so a second
// instance fails with ErrStateDirLocked instead of racing the first for
// index.db.
func Open(dir string, opt Options) (*Store, error) {
	return OpenCtx(context.Background(), dir, opt)
}

// OpenCtx is Open with the rebuild and journal-replay statements under ctx.
func OpenCtx(ctx context.Context, dir string, opt Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lk, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		// Also runs on a panic, as process death would release the flock.
		if !opened {
			_ = lk.Close()
		}
	}()
	s, err := openLocked(ctx, dir, opt)
	if err != nil {
		return nil, err
	}
	opened = true
	s.dirLock = lk
	return s, nil
}

func openLocked(ctx context.Context, dir string, opt Options) (*Store, error) {
	if opt.FS == nil {
		opt.FS = fsx.OS{}
	}
	if opt.Obs == nil {
		opt.Obs = obs.New(nil)
	}
	if opt.DriverName == "" {
		opt.DriverName = "sqlite"
	}
	if opt.ReadTimeout <= 0 {
		opt.ReadTimeout = DefaultReadTimeout
	}
	if opt.BulkRecords <= 0 {
		opt.BulkRecords = DefaultBulkRecords
	}
	if opt.BulkBytes <= 0 {
		opt.BulkBytes = DefaultBulkBytes
	}
	if opt.V3Layout {
		opt.NoTrigramIndex, opt.ExtensionTables = true, true
	}
	s := &Store{v3: opt.V3Layout, freeFn: opt.FreeBytes, noTrigram: opt.NoTrigramIndex, wantExt: opt.ExtensionTables, bulkRecords: opt.BulkRecords, bulkBytes: opt.BulkBytes, dir: dir, fs: opt.FS, obs: opt.Obs, driverName: opt.DriverName, readTO: opt.ReadTimeout, stmtCtx: opt.stmtCtx, readCacheKiB: opt.ReadCacheKiB, readMmap: opt.ReadMmapBytes, readMaxOpen: opt.ReadMaxOpenConns, journalLimit: opt.JournalSizeLimit}
	if s.stepClock = opt.StepClock; s.stepClock == nil {
		s.stepClock = monotonicNow
	}
	if s.journalLimit <= 0 {
		s.journalLimit = JournalSizeLimit
	}
	for _, d := range []string{dir, filepath.Join(dir, CopiesDir), filepath.Join(dir, ArchivesDir)} {
		if err := s.fs.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	jpath := filepath.Join(dir, JournalName)
	endPhase := phaseOf(opt.Phase, "store_journal_read")
	var recs []Rec
	var good, torn int64
	var err error
	if opt.decoded != nil {
		recs, good, torn = opt.decoded.recs, opt.decoded.good, opt.decoded.torn
	} else {
		recs, good, torn, err = readJournal(jpath)
	}
	endPhase()
	if err != nil {
		return nil, err
	}
	s.TornBytes = torn
	if torn > 0 {
		if err := os.Truncate(jpath, good); err != nil {
			return nil, err
		}
	}
	for _, r := range recs {
		if r.Seq >= s.nextSeq {
			s.nextSeq = r.Seq + 1
		}
		if r.File >= s.nextFile {
			s.nextFile = r.File + 1
		}
	}
	if s.nextSeq == 0 {
		s.nextSeq = 1
	}
	if s.nextFile == 0 {
		s.nextFile = 1
	}
	dbPath := filepath.Join(dir, DBName)
	_, statErr := os.Stat(dbPath)
	dbExisted := statErr == nil
	if opt.Rebuild && dbExisted {
		if err := s.rebuildPreflight(dbPath); err != nil {
			return nil, err
		}
		for _, suf := range []string{"-wal", "-shm"} {
			_ = os.Remove(dbPath + suf)
		}
		if err := os.Rename(dbPath, dbPath+".pre-rebuild"); err != nil {
			return nil, err
		}
		dbExisted = false
	}
	endPhase = phaseOf(opt.Phase, "store_open_db")
	err = s.openDB(dbPath, opt.BulkRebuild)
	endPhase()
	if err != nil {
		var older *SchemaOlderError
		if !dbExisted || errors.As(err, &older) || !isCorruptDBError(err) {
			// Only genuine corruption sets the file aside; a busy or locked
			// error means a live process holds it and must fail the open.
			return nil, err
		}
		// A corrupt index.db: keep it aside for diagnosis and rebuild from the journal.
		s.closeDBs()
		for _, suf := range []string{"-wal", "-shm"} {
			_ = os.Remove(dbPath + suf)
		}
		if err := os.Rename(dbPath, dbPath+".corrupt"); err != nil {
			return nil, err
		}
		dbExisted = false
		if err := s.openDB(dbPath, opt.BulkRebuild); err != nil {
			return nil, err
		}
	}
	if !opt.BulkRebuild && s.metaString(metaRebuildUnfinished) != "" {
		s.closeDBs()
		return nil, &UnfinishedRebuildError{}
	}
	if opt.BulkRebuild {
		if err := s.beginRebuild(ctx); err != nil {
			s.closeDBs()
			return nil, err
		}
	}
	s.lastSeq = s.metaInt("last_seq")
	if dbExisted && s.lastSeq == 0 && len(recs) > 0 {
		// A database that applied nothing of a non-empty journal is a rebuild
		// that was interrupted before it committed: it is still a rebuild (the
		// epoch must bump so clients resync).
		dbExisted = false
	}
	if v := s.metaInt("schema_version"); v > SchemaVersion {
		_ = s.CloseCtx(ctx)
		return nil, &SchemaNewerError{Found: int(v), Known: SchemaVersion}
	}
	s.noteReplayCollisions(recs)
	endPhase = phaseOf(opt.Phase, "store_replay")
	err = s.replay(ctx, recs)
	endPhase()
	if err != nil {
		_ = s.CloseCtx(ctx)
		return nil, err
	}
	s.Rebuilt = !dbExisted && len(recs) > 0
	if s.Rebuilt {
		s.obs.Reg.Add(obs.MIndexRebuilds, nil, 1)
		s.obs.Event(obs.EvIndexRebuilt, "journal_records", strconv.Itoa(len(recs)), "forced", strconv.FormatBool(opt.Rebuild))
	}
	jf, err := s.fs.OpenFile(jpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = s.CloseCtx(ctx)
		return nil, err
	}
	s.jf = jf
	if !opt.ReplayOnly {
		if cur := s.metaInt("epoch"); cur == 0 || s.Rebuilt {
			if err := s.CommitCtx(ctx, &Rec{T: TEpoch, At: time.Now().UnixNano(), Epoch: int(cur) + 1}); err != nil {
				_ = s.CloseCtx(ctx)
				return nil, err
			}
		}
	}
	return s, nil
}

// phaseOf starts the named startup phase on hook (nil is a no-op) and returns
// its end function.
func phaseOf(hook func(string) func(), name string) func() {
	if hook == nil {
		return func() {}
	}
	if done := hook(name); done != nil {
		return done
	}
	return func() {}
}

// busyTimeoutMS is the SQLite busy_timeout of every connection; a var so a test can shorten it.
var busyTimeoutMS = 5000

func (s *Store) openDB(path string, bulk bool) error {
	dsn := "file:" + path + "?_pragma=journal_mode(" + ablateJournal + ")&_pragma=busy_timeout(" + strconv.Itoa(busyTimeoutMS) + ")&_pragma=synchronous(NORMAL)" + ablateDSNExtra
	// journal_size_limit lives in the DSN so a re-opened pooled connection keeps
	// it; it caps the WAL file left behind when the WAL restarts.
	wdsn := dsn + "&_pragma=journal_size_limit(" + strconv.Itoa(s.journalLimit) + ")"
	cdsn := wdsn
	if bulk {
		wdsn += bulkDSNPragmas()
	}
	w, err := sql.Open(s.driverName, wdsn)
	if err != nil {
		return err
	}
	w.SetMaxOpenConns(1)
	// An older schema is refused before any DDL runs: the idempotent CREATE
	// statements below would not change an existing table's columns.
	var cur string
	if w.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&cur) == nil {
		if v, err := strconv.Atoi(cur); err == nil && v < SchemaVersion {
			_ = w.Close()
			return &SchemaOlderError{Found: v, Known: SchemaVersion}
		}
	}
	if err := dropFullFieldsIndex(w); err != nil {
		_ = w.Close()
		return err
	}
	if err := addSessionRefColumn(w); err != nil {
		_ = w.Close()
		return err
	}
	if err := addLookupColumns(w); err != nil {
		_ = w.Close()
		return err
	}
	if err := addLastCopyColumn(w); err != nil {
		_ = w.Close()
		return err
	}
	// One transaction: one commit for the whole script instead of one per
	// statement (the FTS5 tables add many).
	var hasTri int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fts_tri'`).Scan(&hasTri); err != nil {
		_ = w.Close()
		return err
	}
	var hasRecords int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&hasRecords); err != nil {
		_ = w.Close()
		return err
	}
	// An existing file keeps the layout it was built with (a reopen without the
	// layout option must not add back a table the file was built without); the
	// options only pick the layout of a file that has none.
	s.trigram = hasTri > 0 || (hasRecords == 0 && !s.noTrigram)
	var hasExt int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, extTables[0].Table()).Scan(&hasExt); err != nil {
		_ = w.Close()
		return err
	}
	s.ext = hasExt > 0 || s.wantExt
	sessText, err := recordsHasSessionText(w, s.v3)
	if err != nil {
		_ = w.Close()
		return err
	}
	s.sessText = sessText
	tables := derivedTables(s.trigram, s.ext, sessText)
	ddl := schemaSQL + tables + layoutIndexesSQL(sessText)
	if bulk {
		// A bulk rebuild creates the secondary indexes after the load.
		ddl = schemaSQL + tables
	}
	// An index.db from before unknown_keys_total existed gets the aggregate
	// backfilled in the transaction that creates the table.
	var hasTotal int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='unknown_keys_total'`).Scan(&hasTotal); err != nil {
		_ = w.Close()
		return err
	}
	if hasTotal == 0 {
		var hasKeys int
		if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='unknown_keys'`).Scan(&hasKeys); err != nil {
			_ = w.Close()
			return err
		}
		if hasKeys > 0 {
			ddl += unknownTotalBackfillSQL
		}
	}
	// A database created empty by this code has session_ref on every row, so it
	// is marked and gets the session_ref indexes with its (empty) records table;
	// a database that already had rows is never marked (see metaSessionRef).
	marked, err := sessionRefMarked(w)
	if err != nil {
		_ = w.Close()
		return err
	}
	if marked {
		ddl += `INSERT OR IGNORE INTO meta(key,value) VALUES('` + metaSessionRef + `','1');` + "\n"
		if !bulk {
			ddl += sessionRefIndexesSQL()
		}
	}
	// A database created empty by this code has every file's facts from its
	// first record on; one that already had records is backfilled in the
	// background and marked when that finishes (see filefacts.go).
	if hasRecords == 0 {
		ddl += `INSERT OR IGNORE INTO meta(key,value) VALUES('` + MetaFileFactsComplete + `','1');` + "\n"
		ddl += `INSERT OR IGNORE INTO meta(key,value) VALUES('` + MetaAskFactsComplete + `','1');` + "\n"
	}
	if err := execInTx(w, ddl); err != nil {
		_ = w.Close()
		return err
	}
	if _, err := w.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version', ?)`, strconv.Itoa(SchemaVersion)); err != nil {
		_ = w.Close()
		return err
	}
	r, err := openDeadlineDB(s.driverName, dsn+s.readPragmas(), s.readTO, s.stmtCtx)
	if err != nil {
		_ = w.Close()
		return err
	}
	r.SetConnMaxLifetime(readConnMaxLifetime)
	r.SetConnMaxIdleTime(readConnMaxIdleTime)
	switch {
	case s.readMaxOpen == 0:
		r.SetMaxOpenConns(DefaultReadMaxOpenConns)
	case s.readMaxOpen > 0:
		r.SetMaxOpenConns(s.readMaxOpen)
	}
	r.SetMaxIdleConns(readMaxIdleConns)
	s.Reader = Reader{q: r}
	// A passive checkpoint can run for minutes on a large backlog: it gets its
	// own connection so it never holds the one write connection.
	c, err := sql.Open(s.driverName, cdsn)
	if err != nil {
		_ = w.Close()
		_ = r.Close()
		return err
	}
	c.SetMaxOpenConns(1)
	s.wdb, s.rdb, s.cdb = w, r, c
	s.deferIdx = bulk
	s.sessionRef = marked
	return nil
}

// recordsHasSessionText reports whether records carries the legacy session
// column: an existing table by its columns, a new one by the layout wanted.
func recordsHasSessionText(db *sql.DB, v3 bool) (bool, error) {
	var tbl int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&tbl); err != nil {
		return false, err
	}
	if tbl == 0 {
		return !v3, nil
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name='session'`).Scan(&n)
	return n > 0, err
}

// HasSessionText reports whether records has the legacy session TEXT column. The
// version 3 layout has none: every reader goes through session_ref.
func (s *Store) HasSessionText() bool { return s.sessText }

// sessionRefMarked reports whether the database being opened is marked, or is
// new (no records table yet) and so is marked by this open.
func sessionRefMarked(db *sql.DB) (bool, error) {
	var tbl int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&tbl); err != nil {
		return false, err
	}
	if tbl == 0 {
		return true, nil
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, metaSessionRef).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "1", err
}

// SessionRefReady reports whether every records row of this store carries a
// session_ref with a row in sessions, so a query may read session_ref instead of
// records.session (and use the session_ref indexes). It is false for a database
// that existed before session_ref was written: its older rows have session_ref 0
// until it is rebuilt, and a query that read session_ref there would miss them.
func (s *Store) SessionRefReady() bool { return s.sessionRef }

// JournalSizeLimit is the journal_size_limit pragma, in bytes: the WAL file is
// truncated to at most this size when the WAL restarts.
const JournalSizeLimit = 1073741824

// Checkpoint runs one PASSIVE WAL checkpoint on its own connection and returns
// the frames in the WAL (log) and the frames already moved into the database
// (checkpointed). Both are cumulative until the WAL restarts; checkpointed ==
// log means complete, less means partial (a reader pins the rest). It does not
// take the write lock, so it is safe beside a writer. It returns an error
// after Close.
func (s *Store) Checkpoint() (log, checkpointed int, err error) {
	s.mu.Lock()
	c := s.cdb
	s.mu.Unlock()
	if c == nil {
		return 0, 0, errors.New("store closed")
	}
	var busy int
	if err := c.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log, &checkpointed); err != nil {
		return 0, 0, err
	}
	return log, checkpointed, nil
}

// walBusyTimeoutMS is the busy_timeout the checkpoint connection runs with
// (the DSN value); CheckpointTruncate restores it after its own, shorter one.
const walBusyTimeoutMS = 5000

// CheckpointTruncate runs one PRAGMA wal_checkpoint(TRUNCATE) on the checkpoint
// connection, waiting at most busyTimeout for readers. A TRUNCATE takes the WAL
// write lock while it waits, so a writer blocks for as long as it does: the
// caller MUST bound how often it runs and run it only when a fresh passive
// checkpoint reached every frame. busy is true when a reader pinned the WAL (or
// a writer held it) and the attempt gave up; the WAL is then unchanged. It
// returns an error after Close.
func (s *Store) CheckpointTruncate(busyTimeout time.Duration) (busy bool, err error) {
	s.mu.Lock()
	c := s.cdb
	s.mu.Unlock()
	if c == nil {
		return false, errors.New("store closed")
	}
	ctx := context.Background()
	conn, err := c.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=`+strconv.FormatInt(busyTimeout.Milliseconds(), 10)); err != nil {
		return false, err
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA busy_timeout=`+strconv.Itoa(walBusyTimeoutMS)) }()
	var b, log, done int
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&b, &log, &done); err != nil {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "BUSY") {
			return true, nil
		}
		return false, err
	}
	return b != 0, nil
}

func (s *Store) closeDBs() {
	if s.btx != nil {
		s.rbEpoch.Add(1)
		_ = s.btx.Rollback()
		s.btx = nil
	}
	if s.cdb != nil {
		_ = s.cdb.Close()
	}
	if s.wdb != nil {
		_ = s.wdb.Close()
	}
	if s.rdb != nil {
		_ = s.rdb.Close()
	}
	s.wdb, s.rdb, s.cdb = nil, nil, nil
}

func (s *Store) metaInt(key string) int64 {
	var v string
	if err := s.wdb.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

// FS returns the state-directory file system.
func (s *Store) FS() fsx.FS { return s.fs }

// Epoch returns the current state epoch; a rebuild bumps it so clients resync.
func (s *Store) Epoch() int { return int(s.metaInt("epoch")) }

// CommitSeq counts the journal applies committed by this process. The file,
// path and generation rows change only inside one, so a reader that cached them
// rebuilds only when it moved. It issues no query.
func (s *Store) CommitSeq() uint64 { return s.commitSeq.Load() }

// PathSeq counts the journal applies committed by this process that held a
// record able to change the paths table (a path, a path close or a tombstone).
// A reader that cached the current paths rebuilds only when it moved; it follows
// the read-before-query rule of LiveInputSeq and issues no query.
func (s *Store) PathSeq() uint64 { return s.pathSeq.Load() }

// LiveInputSeq counts the commits that may have changed what decides which
// Conversations hold a live Process: a session-registry record (a WithWrite or
// bulk-batch commit that followed NoteLiveInput) and a journal apply holding a
// Process observation. It moves after the commit is visible to readers, so a
// reader that reads it before it queries and caches the answer under that value
// never keeps a stale one: a write that lands later moves the value and the next
// caller reloads. It may move without a change (a rolled-back write leaves the
// mark for the next commit), never the other way. It issues no query.
func (s *Store) LiveInputSeq() uint64 { return s.liveSeq.Load() }

// NoteLiveInput marks the write in flight as storing a live-Process input (a
// session-registry record); the commit of that write moves LiveInputSeq. It is
// called inside the WithWrite callback.
func (s *Store) NoteLiveInput() { s.livePend.Store(true) }

// ModelInputSeq counts the commits that may have changed any input of the
// lifecycle model: everything LiveInputSeq covers, plus history and control-end
// records, a rewritten generation's cleared rows and Conversation stage rows. It
// follows the same read-before-query rule as LiveInputSeq and issues no query.
func (s *Store) ModelInputSeq() uint64 { return s.modelSeq.Load() }

// NoteModelInput marks the write in flight as storing a lifecycle model input
// that is not a live-Process input (a history or control-end record, or rows
// cleared from a generation); the commit of that write moves ModelInputSeq.
func (s *Store) NoteModelInput() { s.modelPend.Store(true) }

// RemovalSeq counts the commits that may have removed record rows (a rewritten
// generation's cleared rows). Only such a commit can take away a record a
// reader already folded, so a reader that folds records in id order and keeps
// them may extend its state while RemovalSeq is unchanged. It follows the
// read-before-query rule of LiveInputSeq and issues no query.
func (s *Store) RemovalSeq() uint64 { return s.removeSeq.Load() }

// NoteRowsRemoved marks the write in flight as removing record rows; the commit
// of that write moves RemovalSeq (and ModelInputSeq, as NoteModelInput does).
func (s *Store) NoteRowsRemoved() { s.removePend.Store(true); s.modelPend.Store(true) }

// bumpLive moves LiveInputSeq when a committed write was marked by NoteLiveInput
// and ModelInputSeq when it was marked by either mark.
func (s *Store) bumpLive() {
	l := s.livePend.Swap(false)
	m := s.modelPend.Swap(false)
	if s.removePend.Swap(false) {
		s.removeSeq.Add(1)
	}
	if l {
		s.liveSeq.Add(1)
	}
	if l || m {
		s.modelSeq.Add(1)
	}
}

// LastSeq returns the last journal sequence applied to the database.
func (s *Store) LastSeq() int64 { return s.metaInt("last_seq") }

// SchemaVersion returns the schema_version recorded in index.db.
// HasTrigram reports whether this index.db has the trigram table fts_tri. When
// it does not, opaque fields are indexed in the word index.
func (s *Store) HasTrigram() bool { return s.trigram }

func (s *Store) SchemaVersion() int { return int(s.metaInt("schema_version")) }

// OnCommit registers a hook called after each successful database commit (the
// recording file-system wrapper observes the commit through it).
func (s *Store) OnCommit(fn func()) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// OnWrite registers a hook called after each successful WithWrite commit (the
// parser's record commits). A hook runs under the write lock and MUST NOT block.
func (s *Store) OnWrite(fn func()) {
	s.mu.Lock()
	s.wrHooks = append(s.wrHooks, fn)
	s.mu.Unlock()
}

// OffsetsHook is called when a generation's copy offset or parse offset has
// changed: after the commit that changed it, still under the write lock, with
// the generation's committed offsets. at is the commit's seen time for a copy
// change (the journal commit record's time) and the store clock for a parse
// change. A hook MUST NOT block, MUST be idempotent (the same offsets may be
// delivered twice) and MAY assume neither offset ever decreases.
type OffsetsHook func(file int64, gen int, copyOffset, parseOffset int64, at time.Time)

// offNote is one offset change waiting to be delivered. A copy note carries
// both offsets (read inside the applying transaction); a parse note is resolved
// after the commit (read false).
type offNote struct {
	file        int64
	gen         int
	copy, parse int64
	at          time.Time
	read        bool // copy and parse still to be read from the database
	fileRec     bool // a file-row change: the file's source and state are read after the commit
}

// FileStateHook is called, inside the locked apply that creates a file row or
// changes its state, with the committed source and state of the file (state
// "deleted" included). A hook MUST NOT block and MUST be idempotent.
type FileStateHook func(file int64, source, state string)

// OnFileState registers a hook for file-row changes: a file record (TFile), a
// state change (TState), a tombstone (TTomb), a snapshot (TSnap) or an identity
// update (TIdent, which revives a deleted file). Journal replay at open is
// excepted: a subscriber seeds itself.
func (s *Store) OnFileState(fn FileStateHook) {
	s.mu.Lock()
	s.fileHooks = append(s.fileHooks, fn)
	s.mu.Unlock()
}

// OnOffsets registers a hook called inside the locked apply that changes a
// generation's copy offset (a copy commit, journal replay after open excepted:
// a subscriber seeds itself) or parse offset (the parser's transactions, tail
// mode, ReparseGen, SetParseOffset). A hook runs under the write lock and MUST
// NOT block.
func (s *Store) OnOffsets(fn OffsetsHook) {
	s.mu.Lock()
	s.offHooks = append(s.offHooks, fn)
	s.mu.Unlock()
}

// NoteParseOffset records, from inside a WithWrite callback, that the callback
// advanced the parse offset of a generation; the OnOffsets hooks run after the
// transaction commits, with the committed offsets. A callback whose UPDATE
// matched no row (the parse_offset <= copy_offset guard) MUST NOT call it.
func (s *Store) NoteParseOffset(file int64, gen int) {
	s.pendMu.Lock()
	s.pendOff = append(s.pendOff, offNote{file: file, gen: gen, read: true})
	s.pendMu.Unlock()
}

// pendLen and pendTrunc let a savepoint rollback drop the notes of the failed
// callback.
func (s *Store) pendLen() int {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	return len(s.pendOff)
}

func (s *Store) pendTrunc(n int) {
	s.pendMu.Lock()
	if n < len(s.pendOff) {
		s.pendOff = s.pendOff[:n]
	}
	s.pendMu.Unlock()
}

// fireOffsets delivers the notes to the OnOffsets hooks. The caller holds the
// write lock, no transaction of its own is open on the write handle that would
// block a read, and the notes' writes are committed.
func (s *Store) fireOffsets(notes []offNote) {
	if len(s.offHooks) == 0 && len(s.fileHooks) == 0 {
		return
	}
	for _, n := range notes {
		if n.fileRec {
			if s.wdb == nil || len(s.fileHooks) == 0 {
				continue
			}
			var src, state string
			if err := s.wdb.QueryRow(`SELECT source,state FROM files WHERE id=?`, n.file).Scan(&src, &state); err != nil {
				continue
			}
			for _, h := range s.fileHooks {
				h(n.file, src, state)
			}
			continue
		}
		if n.read {
			if s.wdb == nil {
				continue
			}
			if err := s.wdb.QueryRow(`SELECT copy_offset,parse_offset FROM generations WHERE file_id=? AND gen=?`, n.file, n.gen).Scan(&n.copy, &n.parse); err != nil {
				continue
			}
			if n.at.IsZero() {
				n.at = time.Now()
			}
		}
		for _, h := range s.offHooks {
			h(n.file, n.gen, n.copy, n.parse, n.at)
		}
	}
}

// takePend returns and clears the notes of the write that just committed.
func (s *Store) takePend() []offNote {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	n := s.pendOff
	s.pendOff = nil
	return n
}

func (s *Store) dropPend() { s.pendMu.Lock(); s.pendOff = nil; s.pendMu.Unlock() }

// NewFileID reserves a file id. Ids name copies, not Claude paths.
func (s *Store) NewFileID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextFile
	s.nextFile++
	return id
}

// CopyPath is the copy file of an append file's generation.
func (s *Store) CopyPath(file int64, gen int) string {
	return filepath.Join(s.dir, CopiesDir, fmt.Sprintf("%d.%d", file, gen))
}

// SnapshotPath is the stored bytes of a snapshot version.
func (s *Store) SnapshotPath(file int64, version int) string {
	return filepath.Join(s.dir, CopiesDir, fmt.Sprintf("%d.s%d", file, version))
}

// TailPath is the small tail sidecar of an append file (kept out of the
// database; after settling the tail no longer sits in a copy file).
func (s *Store) TailPath(file int64) string {
	return filepath.Join(s.dir, CopiesDir, fmt.Sprintf("%d.tail", file))
}

// Commit writes recs to the journal (sequence numbers assigned, fsynced) and
// then applies them to the database in one transaction.
func (s *Store) Commit(recs ...*Rec) error { return s.commit(context.Background(), false, recs) }

// CommitCtx is Commit with the database transaction under ctx. A journalled
// record is replayed at the next open, so a ctx that ends between the journal
// write and the apply loses nothing.
func (s *Store) CommitCtx(ctx context.Context, recs ...*Rec) error {
	return s.commit(ctx, false, recs)
}

// CommitFull is Commit with F_FULLFSYNC on the journal and PRAGMA fullfsync=ON
// for the database commit, for the records a delete depends on ("settled",
// "removal pending", "removed"). On darwin the plain fsync of Commit does not
// flush the drive's cache.
func (s *Store) CommitFull(recs ...*Rec) error { return s.commit(context.Background(), true, recs) }

// CommitFullCtx is CommitFull with the database transaction under ctx.
func (s *Store) CommitFullCtx(ctx context.Context, recs ...*Rec) error {
	return s.commit(ctx, true, recs)
}

// lockWrite takes the single write lock. A contended acquisition observes the
// seconds the caller queued (store_write_lock_wait_seconds); an uncontended one
// reads no clock and touches no registry lock.
func (s *Store) lockWrite() {
	if s.mu.TryLock() {
		return
	}
	now := s.waitNow
	if now == nil {
		now = time.Now
	}
	start := now()
	if s.onContended != nil {
		s.onContended()
	}
	s.mu.Lock()
	s.obs.Reg.Observe(obs.MStoreWriteLockWait, now().Sub(start).Seconds())
}

func (s *Store) commit(ctx context.Context, full bool, recs []*Rec) error {
	s.lockWrite()
	defer s.mu.Unlock()
	if err := s.checkFileIDs(recs); err != nil {
		return err
	}
	var buf []byte
	for _, r := range recs {
		r.Seq = s.nextSeq
		s.nextSeq++
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf = append(append(buf, b...), '\n')
	}
	if _, err := s.jf.Write(buf); err != nil {
		return err
	}
	if full {
		if err := fsx.FullSync(s.jf); err != nil {
			return err
		}
	} else if err := s.jf.Sync(); err != nil {
		return err
	}
	faults.At("store.after-journal")
	if full {
		// The index runs at synchronous=NORMAL; a commit a delete depends on is
		// FULL on a pinned connection, so the setting cannot land on a different
		// pooled connection than the transaction.
		conn, err := s.wdb.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		restore := s.durabilityPragmas(ctx, conn, true)
		defer restore()
		if err := s.applyAllOn(ctx, conn, recs); err != nil {
			return err
		}
	} else if err := s.applyAll(ctx, recs); err != nil {
		return err
	}
	for _, h := range s.hooks {
		h()
	}
	return nil
}

// FileIDCollisionError is a new file record whose id already names a file of a
// different mode or source.
type FileIDCollisionError struct {
	ID                        int64
	Mode, Source              string
	ExistingMode, ExistingSrc string
}

func (e *FileIDCollisionError) Error() string {
	return fmt.Sprintf("file id %d already exists as %s/%s; refusing %s/%s", e.ID, e.ExistingSrc, e.ExistingMode, e.Source, e.Mode)
}

// checkFileIDs rejects, before anything reaches the journal, a file record
// whose id already exists (in the index or earlier in recs) with a different
// mode or source. A same-mode, same-source repeat stays an idempotent no-op.
// Replay does not call it: a journal that already holds a collision must still
// open (see noteReplayCollisions).
func (s *Store) checkFileIDs(recs []*Rec) error {
	var seen map[int64]*Rec
	for _, r := range recs {
		if r.T != TFile {
			continue
		}
		mode, src, found := "", "", false
		if prev, ok := seen[r.File]; ok {
			mode, src, found = prev.Mode, prev.Source, true
		} else if err := s.wdb.QueryRow(`SELECT mode, source FROM files WHERE id=?`, r.File).Scan(&mode, &src); err == nil {
			found = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if found && (mode != r.Mode || src != r.Source) {
			s.obs.Reg.Add(obs.MFileIDCollisions, map[string]string{"where": "write"}, 1)
			s.obs.Event(obs.EvFileIDCollision, "where", "write", "file", strconv.FormatInt(r.File, 10),
				"existing", src+"/"+mode, "new", r.Source+"/"+r.Mode)
			return &FileIDCollisionError{ID: r.File, Mode: r.Mode, Source: r.Source, ExistingMode: mode, ExistingSrc: src}
		}
		if seen == nil {
			seen = map[int64]*Rec{}
		}
		seen[r.File] = r
	}
	return nil
}

// noteReplayCollisions records, once per id, journal file records that disagree
// with the first record of that id on mode or source. The replay itself is
// unchanged (INSERT OR IGNORE keeps the first), so a journal that already holds
// reissued ids still opens.
func (s *Store) noteReplayCollisions(recs []Rec) {
	first := map[int64]*Rec{}
	done := map[int64]bool{}
	for i := range recs {
		r := &recs[i]
		if r.T != TFile {
			continue
		}
		f, ok := first[r.File]
		if !ok {
			first[r.File] = r
			continue
		}
		if (f.Mode != r.Mode || f.Source != r.Source) && !done[r.File] {
			done[r.File] = true
			s.ReplayCollisions = append(s.ReplayCollisions, r.File)
			s.obs.Reg.Add(obs.MFileIDCollisions, map[string]string{"where": "replay"}, 1)
			s.obs.Event(obs.EvFileIDCollision, "where", "replay", "file", strconv.FormatInt(r.File, 10),
				"existing", f.Source+"/"+f.Mode, "new", r.Source+"/"+r.Mode)
		}
	}
}

// txBeginner is what applyAll opens its transaction on: the write handle, or a
// pinned connection of it.
type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// durabilityPragmas makes conn FULL-synchronous with F_FULLFSYNC for the
// database file and for checkpoints (the sync of a checkpoint is its own pragma
// on darwin), and returns the function that puts the connection back to the
// store's running setting (NORMAL, or OFF during a bulk rebuild).
func (s *Store) durabilityPragmas(ctx context.Context, conn *sql.Conn, withCheckpoint bool) func() {
	on := []string{`PRAGMA synchronous=FULL`, `PRAGMA fullfsync=ON`}
	off := []string{`PRAGMA fullfsync=OFF`}
	if withCheckpoint {
		on = append(on, `PRAGMA checkpoint_fullfsync=ON`)
		off = append(off, `PRAGMA checkpoint_fullfsync=OFF`)
	}
	rest := `PRAGMA synchronous=NORMAL`
	if s.bulk {
		rest = `PRAGMA synchronous=OFF`
	}
	for _, q := range on {
		_, _ = conn.ExecContext(ctx, q)
	}
	return func() {
		for _, q := range append(off, rest) {
			_, _ = conn.ExecContext(ctx, q)
		}
	}
}

func (s *Store) applyAll(ctx context.Context, recs []*Rec) error {
	return s.applyAllOn(ctx, s.wdb, recs)
}

func (s *Store) applyAllOn(ctx context.Context, db txBeginner, recs []*Rec) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var notes []offNote
	for _, r := range recs {
		if err := apply(tx, r, &notes); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply seq %d (%s): %w", r.Seq, r.T, err)
		}
	}
	last := recs[len(recs)-1].Seq
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('last_seq',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(last, 10)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.lastSeq = last
	s.commitSeq.Add(1)
	var proc, model, paths bool
	for _, r := range recs {
		switch r.T {
		case TPath, TPathClose, TTomb:
			paths = true
		case TProc:
			proc = true
		case TStage, TSettle, TTorn:
			model = true
		}
	}
	if paths {
		s.pathSeq.Add(1)
	}
	if proc {
		s.liveSeq.Add(1)
	}
	if proc || model {
		s.modelSeq.Add(1)
	}
	s.noteFiles(recs)
	s.fireOffsets(notes)
	return nil
}

// ErrBarrierBusy means the removal barrier could not checkpoint the whole WAL
// because a reader still pins an older snapshot of it.
var ErrBarrierBusy = errors.New("store: the WAL could not be fully checkpointed (a reader pins it)")

// barrierBusyTimeoutMS bounds how long the barrier waits for a reader before it
// reports ErrBarrierBusy, so the removal pass is refused, not stalled.
const barrierBusyTimeoutMS = 250

// Barrier is the durability barrier that runs before the recorder lets an
// original be removed from Claude's directories. Normal running keeps the index
// at synchronous=NORMAL, so the tail of recent commits may sit only in the WAL
// and the OS cache; the barrier makes everything committed so far durable: the
// journal (F_FULLFSYNC), then a TRUNCATE checkpoint under synchronous=FULL with
// fullfsync and checkpoint_fullfsync, then F_FULLFSYNC of the database file
// itself. It returns ErrBarrierBusy when a reader keeps the checkpoint from
// finishing; the caller MUST NOT remove anything on any error.
func (s *Store) Barrier() error { return s.BarrierCtx(context.Background()) }

// BarrierCtx is Barrier with its checkpoint connection taken under ctx.
func (s *Store) BarrierCtx(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wdb == nil {
		return errors.New("store closed")
	}
	if s.jf != nil {
		if err := fsx.FullSync(s.jf); err != nil {
			return err
		}
	}
	faults.At("store.before-barrier-checkpoint")
	conn, err := s.wdb.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	restore := s.durabilityPragmas(ctx, conn, true)
	defer restore()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=`+strconv.Itoa(barrierBusyTimeoutMS)); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`) }()
	var busy, log, ckpt int
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &ckpt); err != nil {
		return err
	}
	if busy != 0 {
		return ErrBarrierBusy
	}
	f, err := s.fs.OpenFile(filepath.Join(s.dir, DBName), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = fsx.FullSync(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// replay applies journal records the database has not seen. It is idempotent.
func (s *Store) replay(ctx context.Context, recs []Rec) error {
	var pending []*Rec
	for i := range recs {
		if recs[i].Seq > s.lastSeq {
			pending = append(pending, &recs[i])
		}
	}
	if len(pending) == 0 {
		return nil
	}
	faults.At("store.replay")
	return s.applyAll(ctx, pending)
}

// Close bounds, so a stuck write or reader cannot hold shutdown past the
// daemon's deadline: closeLockWait for the store lock, closeCheckpointWait for
// the closing WAL truncate (skipped on timeout: the WAL replays on the next
// open) and closeDBsWait for closing the connections (the last one checkpoints
// implicitly). They are vars so a test can shorten them.
var (
	closeLockWait       = 2 * time.Second
	closeCheckpointWait = 3 * time.Second
	closeStatsWait      = 2 * time.Second
	closeDBsWait        = 3 * time.Second
)

// errCloseBusy is Close's error when a bound ran out. The state directory lock
// is then left to process exit (a lock released under a live write would let a
// second instance start against it), and the journal and the WAL recover on
// the next open.
var errCloseBusy = errors.New("store close: timed out; the state lock is left to process exit")

// lockWithin takes s.mu, giving up after d.
func (s *Store) lockWithin(d time.Duration) bool {
	if s.mu.TryLock() {
		return true
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
			if s.mu.TryLock() {
				return true
			}
		}
	}
}

// Close closes the store. It does not delete anything.
func (s *Store) Close() error { return s.CloseCtx(context.Background()) }

// CloseCtx is Close with the bounded stats and checkpoint steps run under ctx
// (each still has its own deadline). A caller that is shutting down passes a
// context that outlives its own cancellation, or both steps are skipped.
func (s *Store) CloseCtx(ctx context.Context) error {
	if !s.lockWithin(closeLockWait) {
		return errCloseBusy
	}
	defer s.mu.Unlock()
	var first error
	if s.jf != nil {
		first = s.jf.Close()
		s.jf = nil
	}
	if s.cdb != nil {
		// Keep sqlite_stat1 current: a due collection runs here, bounded, on the
		// checkpoint connection; a cut-short pass resumes at the next one.
		if s.wdb != nil && s.CollectStatsDue(time.Now(), StatsEvery) {
			ctx, cancel := context.WithTimeout(ctx, closeStatsWait)
			_, _ = s.collectStats(ctx, s.cdb, DefaultAnalysisLimit, time.Now(), 0)
			cancel()
		}
		_ = s.cdb.Close()
		s.cdb = nil
	}
	if s.wdb != nil {
		// Checkpoint so a reopen starts from a small WAL; a reader that pins the
		// WAL makes this wait, so it runs under a deadline and is skipped on it.
		start := time.Now()
		s.obs.Event(obs.EvWALTruncateStart, "source", "close")
		ctx, cancel := context.WithTimeout(ctx, closeCheckpointWait)
		_, _ = s.wdb.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		cancel()
		s.obs.Event(obs.EvWALTruncateDone, "source", "close", "duration_ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
	}
	closed := make(chan struct{})
	go func() { s.closeDBs(); close(closed) }()
	t := time.NewTimer(closeDBsWait)
	defer t.Stop()
	select {
	case <-closed:
	case <-t.C:
		return errors.Join(first, errCloseBusy)
	}
	if s.dirLock != nil {
		_ = s.dirLock.Close()
		s.dirLock = nil
	}
	return first
}

func nz(b bool) int {
	if b {
		return 1
	}
	return 0
}

func resRows(res sql.Result, err error) (int64, error) {
	if err != nil || res == nil {
		return 0, err
	}
	return res.RowsAffected()
}

func apply(tx *sql.Tx, r *Rec, notes *[]offNote) error {
	err := applyRec(tx, r, notes)
	if err == nil {
		switch r.T {
		case TFile, TState, TTomb, TSnap, TIdent:
			*notes = append(*notes, offNote{file: r.File, fileRec: true})
		}
	}
	return err
}

func applyRec(tx *sql.Tx, r *Rec, notes *[]offNote) error {
	var err error
	switch r.T {
	case TEpoch:
		_, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('epoch',?) ON CONFLICT(key) DO UPDATE SET value=MAX(CAST(value AS INTEGER), CAST(excluded.value AS INTEGER))`, strconv.Itoa(r.Epoch))
	case TFile:
		_, err = tx.Exec(`INSERT OR IGNORE INTO files(id,source,mode,dev,ino,birth_ns,first_hash,link_target,first_seen_ns,last_size) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			r.File, r.Source, r.Mode, int64(r.Dev), int64(r.Ino), r.Birth, int64(r.FirstHash), r.Link, r.At, r.Size)
	case TIdent:
		_, err = tx.Exec(`UPDATE files SET dev=?, ino=?, birth_ns=?, deleted_ns=0, state=CASE WHEN state='deleted' THEN 'ok' ELSE state END WHERE id=?`,
			int64(r.Dev), int64(r.Ino), r.Birth, r.File)
		if err == nil && r.FirstHash != 0 {
			_, err = tx.Exec(`UPDATE files SET first_hash=? WHERE id=?`, int64(r.FirstHash), r.File)
		}
	case TPath:
		_, err = tx.Exec(`INSERT OR IGNORE INTO paths(file_id,path,first_seen_ns) VALUES(?,?,?)`, r.File, r.Path, r.At)
	case TPathClose:
		_, err = tx.Exec(`UPDATE paths SET closed_ns=? WHERE file_id=? AND path=? AND closed_ns IS NULL`, r.At, r.File, r.Path)
	case TGen:
		_, err = tx.Exec(`INSERT OR IGNORE INTO generations(file_id,gen,method,reason,start_ns) VALUES(?,?,?,?,?)`, r.File, r.Gen, r.Method, r.Reason, r.At)
		if err == nil {
			_, err = tx.Exec(`UPDATE files SET cur_gen=MAX(cur_gen,?) WHERE id=?`, r.Gen, r.File)
		}
		if err == nil && r.Reset {
			_, err = tx.Exec(`INSERT OR IGNORE INTO resets(id,file_id,from_gen,to_gen,reason,at_ns,pinned) VALUES(?,?,?,?,?,?,1)`, r.Seq, r.File, r.Gen-1, r.Gen, r.Reason, r.At)
		}
	case TCommit:
		{
			var res sql.Result
			res, err = tx.Exec(`UPDATE generations SET copy_offset=?, tail_hash=?, tail_len=? WHERE file_id=? AND gen=? AND copy_offset<=?`,
				r.To, int64(r.Tail), r.TailLen, r.File, r.Gen, r.To)
			if n, _ := resRows(res, err); n > 0 {
				note := offNote{file: r.File, gen: r.Gen, copy: r.To, at: time.Unix(0, r.At)}
				err = tx.QueryRow(`SELECT parse_offset FROM generations WHERE file_id=? AND gen=?`, r.File, r.Gen).Scan(&note.parse)
				*notes = append(*notes, note)
			}
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE files SET last_size=?, last_copy_ns=MAX(last_copy_ns,?) WHERE id=?`, r.Size, r.At, r.File)
		}
	case TSnap:
		_, err = tx.Exec(`INSERT OR IGNORE INTO snapshots(file_id,version,hash,size,seen_ns) VALUES(?,?,?,?,?)`, r.File, r.Version, int64(r.Hash), r.Size, r.At)
		if err == nil {
			_, err = tx.Exec(`UPDATE files SET last_size=?, cur_gen=MAX(cur_gen,?), deleted_ns=0, state='ok' WHERE id=?`, r.Size, r.Version, r.File)
		}
	case TTomb:
		_, err = tx.Exec(`INSERT OR IGNORE INTO tombstones(file_id,path,at_ns) VALUES(?,?,?)`, r.File, r.Path, r.At)
		if err == nil {
			_, err = tx.Exec(`UPDATE files SET deleted_ns=?, state='deleted' WHERE id=?`, r.At, r.File)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE paths SET closed_ns=? WHERE file_id=? AND closed_ns IS NULL`, r.At, r.File)
		}
	case TState:
		_, err = tx.Exec(`UPDATE files SET state=? WHERE id=?`, r.State, r.File)
	case TRelease:
		_, err = tx.Exec(`UPDATE resets SET pinned=0 WHERE id<=?`, r.Version)
	case TProc:
		err = applyProc(tx, r)
	case TStage, TSettle, TTorn:
		err = applySettle(tx, r)
	default:
		err = fmt.Errorf("unknown record type %q", r.T)
	}
	return err
}

// IsDBError reports whether err looks like a corrupt database.
func IsDBError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "file is not a database")
}

var errNotFound = errors.New("store: not found")

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errNotFound

// dropFullFieldsIndex drops fields_name_n when an earlier build of the version
// 2 schema created it as a full index: CREATE INDEX IF NOT EXISTS in derivedIndexes
// would keep it, and the full index is the size the partial one exists to
// avoid. derivedIndexes then recreates it partial. A partial index (its SQL has a
// WHERE clause) is left alone, so a normal open costs one catalog read.
func dropFullFieldsIndex(db *sql.DB) error {
	var def sql.NullString
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='fields_name_n'`).Scan(&def)
	if err == sql.ErrNoRows || (err == nil && strings.Contains(strings.ToUpper(def.String), " WHERE ")) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = db.Exec(`DROP INDEX fields_name_n`)
	return err
}

func execInTx(db *sql.DB, q string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(q); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// addSessionRefColumn gives a records table created before session_ref existed
// the column. The default is a constant, so SQLite adds it without rewriting a
// row; older rows keep 0 (no session row) until the database is rebuilt.
func addSessionRefColumn(db *sql.DB) error {
	var tbl, has int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&tbl); err != nil || tbl == 0 {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name='session_ref'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	_, err := db.Exec(`ALTER TABLE records ADD COLUMN session_ref INTEGER NOT NULL DEFAULT 0`)
	return err
}

// addLastCopyColumn gives a files table created before files.last_copy_ns
// existed the column, and fills it once from the commits table: the newest
// seen_ns of each file's copy commits, which is what applying the journal's
// commit records would have stored. One aggregate pass over commits; measured
// 44 ms on a 13 GB database with 106k commits and 91k files. A file with no
// commit keeps 0. The backfill runs only when the column is added, so a later
// drop of the commits table leaves the column alone.
func addLastCopyColumn(db *sql.DB) error {
	var tbl, has int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='files'`).Scan(&tbl); err != nil || tbl == 0 {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('files') WHERE name='last_copy_ns'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE files ADD COLUMN last_copy_ns INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	var hasCommits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='commits'`).Scan(&hasCommits); err != nil || hasCommits == 0 {
		return err
	}
	_, err := db.Exec(`UPDATE files SET last_copy_ns=c.m FROM (SELECT file_id, MAX(seen_ns) AS m FROM commits GROUP BY file_id) c WHERE files.id=c.file_id`)
	return err
}

// lookupColumns are the records columns that hold an interned lookup id.
var lookupColumns = []struct{ col, table string }{
	{"cwd_id", "lk_cwd"},
	{"branch_id", "lk_branch"},
	{"version_id", "lk_version"},
	{"entrypoint_id", "lk_entrypoint"},
	{"user_type_id", "lk_user_type"},
}

// addLookupColumns gives a records table created before the lookup ids existed
// the five columns (NULL default, so no row is rewritten; older rows keep NULL
// until the database is rebuilt).
func addLookupColumns(db *sql.DB) error {
	var tbl int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&tbl); err != nil || tbl == 0 {
		return err
	}
	for _, c := range lookupColumns {
		var has int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name=?`, c.col).Scan(&has); err != nil {
			return err
		}
		if has > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE records ADD COLUMN ` + c.col + ` INTEGER REFERENCES ` + c.table + `(id)`); err != nil {
			return err
		}
	}
	return nil
}
