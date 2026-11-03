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
// Durability note: the copier's appends use plain fsync, which on darwin does
// not flush the drive's cache. They are therefore weaker than the settle and
// removal path, which commits with PRAGMA fullfsync=ON and flushes with
// F_FULLFSYNC before anything is deleted (later packets). A power loss can leave
// a copy shorter than its committed offset; Recover detects that and marks the
// file copy_mismatch.
//
// Full file contents are never stored in the database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// ReplayOnly opens for comparison: it never appends an epoch record.
	ReplayOnly bool
	// Rebuild forces a rebuild of index.db from the journal: the existing
	// database is set aside (index.db.pre-rebuild) and the feed epoch bumps.
	// It is also the way past a database whose schema_version is newer than
	// this binary knows.
	Rebuild bool
	// DriverName is the database/sql driver name for both handles; empty
	// means "sqlite". A test registers a counting wrapper under another name
	// (internal/store/countdriver) to count reads.
	DriverName string
	// ReadTimeout is the deadline of every read-pool statement and read
	// transaction (DefaultReadTimeout when zero): no reader pins a WAL
	// snapshot for longer.
	ReadTimeout time.Duration
}

// SchemaNewerError is returned by Open when index.db carries a newer
// schema_version than this binary knows.
type SchemaNewerError struct{ Found, Known int }

func (e *SchemaNewerError) Error() string {
	return fmt.Sprintf("index.db schema_version %d is newer than the %d this binary knows; "+
		"upgrade the binary, or start with --rebuild to rebuild the index from the journal", e.Found, e.Known)
}

// Store is an open state directory.
type Store struct {
	driverName string // database/sql driver name (Options.DriverName)
	readTO     time.Duration
	dir        string
	fs         fsx.FS
	obs        *obs.Obs

	wdb *sql.DB // the one serialized write connection
	rdb *sql.DB

	mu       sync.Mutex // serializes every write
	jf       fsx.File
	nextSeq  int64
	nextFile int64
	lastSeq  int64
	hooks    []func()
	wrHooks  []func()

	// TornBytes is the size of the torn journal tail dropped on open.
	TornBytes int64
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
func Open(dir string, opt Options) (*Store, error) {
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
	s := &Store{dir: dir, fs: opt.FS, obs: opt.Obs, driverName: opt.DriverName, readTO: opt.ReadTimeout}
	for _, d := range []string{dir, filepath.Join(dir, CopiesDir), filepath.Join(dir, ArchivesDir)} {
		if err := s.fs.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	jpath := filepath.Join(dir, JournalName)
	recs, good, torn, err := readJournal(jpath)
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
		for _, suf := range []string{"-wal", "-shm"} {
			_ = os.Remove(dbPath + suf)
		}
		if err := os.Rename(dbPath, dbPath+".pre-rebuild"); err != nil {
			return nil, err
		}
		dbExisted = false
	}
	if err := s.openDB(dbPath); err != nil {
		if !dbExisted {
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
		if err := s.openDB(dbPath); err != nil {
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
		_ = s.Close()
		return nil, &SchemaNewerError{Found: int(v), Known: SchemaVersion}
	}
	if err := s.replay(recs); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.Rebuilt = !dbExisted && len(recs) > 0
	if s.Rebuilt {
		s.obs.Reg.Add(obs.MIndexRebuilds, nil, 1)
		s.obs.Event(obs.EvIndexRebuilt, "journal_records", strconv.Itoa(len(recs)), "forced", strconv.FormatBool(opt.Rebuild))
	}
	jf, err := s.fs.OpenFile(jpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.jf = jf
	if !opt.ReplayOnly {
		if cur := s.metaInt("epoch"); cur == 0 || s.Rebuilt {
			if err := s.Commit(&Rec{T: TEpoch, At: time.Now().UnixNano(), Epoch: int(cur) + 1}); err != nil {
				_ = s.Close()
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Store) openDB(path string) error {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"
	w, err := sql.Open(s.driverName, dsn)
	if err != nil {
		return err
	}
	w.SetMaxOpenConns(1)
	// One transaction: with synchronous=FULL each statement of a multi-statement
	// Exec would otherwise sync on its own (the FTS5 tables add many).
	if err := execInTx(w, schemaSQL+derivedSQL); err != nil {
		_ = w.Close()
		return err
	}
	if _, err := w.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version', ?)`, strconv.Itoa(SchemaVersion)); err != nil {
		_ = w.Close()
		return err
	}
	r, err := openDeadlineDB(s.driverName, dsn+"&_pragma=query_only(1)", s.readTO)
	if err != nil {
		_ = w.Close()
		return err
	}
	r.SetConnMaxLifetime(readConnMaxLifetime)
	r.SetConnMaxIdleTime(readConnMaxIdleTime)
	s.wdb, s.rdb = w, r
	return nil
}

func (s *Store) closeDBs() {
	if s.wdb != nil {
		_ = s.wdb.Close()
	}
	if s.rdb != nil {
		_ = s.rdb.Close()
	}
	s.wdb, s.rdb = nil, nil
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

// LastSeq returns the last journal sequence applied to the database.
func (s *Store) LastSeq() int64 { return s.metaInt("last_seq") }

// SchemaVersion returns the schema_version recorded in index.db.
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
func (s *Store) Commit(recs ...*Rec) error { return s.commit(false, recs) }

// CommitFull is Commit with F_FULLFSYNC on the journal and PRAGMA fullfsync=ON
// for the database commit, for the records a delete depends on ("settled",
// "removal pending", "removed"). On darwin the plain fsync of Commit does not
// flush the drive's cache.
func (s *Store) CommitFull(recs ...*Rec) error { return s.commit(true, recs) }

func (s *Store) commit(full bool, recs []*Rec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		if _, err := s.wdb.Exec(`PRAGMA fullfsync=ON`); err != nil {
			return err
		}
		defer func() { _, _ = s.wdb.Exec(`PRAGMA fullfsync=OFF`) }()
	}
	if err := s.applyAll(recs); err != nil {
		return err
	}
	for _, h := range s.hooks {
		h()
	}
	return nil
}

func (s *Store) applyAll(recs []*Rec) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	for _, r := range recs {
		if err := apply(tx, r); err != nil {
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
	return nil
}

// replay applies journal records the database has not seen. It is idempotent.
func (s *Store) replay(recs []Rec) error {
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
	return s.applyAll(pending)
}

// Close closes the store. It does not delete anything.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	if s.jf != nil {
		first = s.jf.Close()
		s.jf = nil
	}
	if s.wdb != nil {
		// Checkpoint so a reopen starts from a small WAL.
		start := time.Now()
		s.obs.Event(obs.EvWALTruncateStart)
		_, _ = s.wdb.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		s.obs.Event(obs.EvWALTruncateDone, "duration_ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
	}
	s.closeDBs()
	return first
}

func nz(b bool) int {
	if b {
		return 1
	}
	return 0
}

func apply(tx *sql.Tx, r *Rec) error {
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
		_, err = tx.Exec(`INSERT OR IGNORE INTO commits(file_id,gen,seq,from_off,to_off,seen_ns) VALUES(?,?,?,?,?,?)`, r.File, r.Gen, r.Seq, r.From, r.To, r.At)
		if err == nil {
			_, err = tx.Exec(`UPDATE generations SET copy_offset=?, tail_hash=?, tail_len=? WHERE file_id=? AND gen=? AND copy_offset<=?`,
				r.To, int64(r.Tail), r.TailLen, r.File, r.Gen, r.To)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE files SET last_size=? WHERE id=?`, r.Size, r.File)
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
