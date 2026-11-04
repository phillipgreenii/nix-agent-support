package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// File is a row of the files table.
type File struct {
	ID        int64
	Source    string
	Mode      string // append, snapshot or link
	Dev, Ino  uint64
	Birth     int64
	FirstHash uint64
	Link      string
	State     string // ok, copy_mismatch or deleted
	FirstSeen int64
	LastSize  int64
	CurGen    int // generation of an append file, latest version of a snapshot file
	DeletedNS int64
}

// Generation is a row of the generations table.
type Generation struct {
	File        int64
	Gen         int
	Method      string
	Reason      string
	StartNS     int64
	CopyOffset  int64
	ParseOffset int64
	TailHash    uint64
	TailLen     int
}

// Snapshot is a stored snapshot version.
type Snapshot struct {
	Version int
	Hash    uint64
	Size    int64
	SeenNS  int64
}

// Reset is a pinned reset entry: a new generation started from byte 0.
type Reset struct {
	ID             int64
	File           int64
	FromGen, ToGen int
	Reason         string
	AtNS           int64
	Pinned         bool
}

const fileCols = `id,source,mode,dev,ino,birth_ns,first_hash,link_target,state,first_seen_ns,last_size,cur_gen,deleted_ns`

func scanFile(sc interface{ Scan(...any) error }) (File, error) {
	var f File
	var dev, ino, fh int64
	err := sc.Scan(&f.ID, &f.Source, &f.Mode, &dev, &ino, &f.Birth, &fh, &f.Link, &f.State, &f.FirstSeen, &f.LastSize, &f.CurGen, &f.DeletedNS)
	f.Dev, f.Ino, f.FirstHash = uint64(dev), uint64(ino), uint64(fh)
	return f, err
}

// File returns the file with the given id.
func (s *Store) File(id int64) (File, error) {
	f, err := scanFile(s.rdb.QueryRow(`SELECT `+fileCols+` FROM files WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	return f, err
}

// FilesByIdentity returns the files with the given device and inode, newest
// first. The caller applies the birth-time and first-bytes rules.
func (s *Store) FilesByIdentity(dev, ino uint64) ([]File, error) {
	return s.files(`SELECT `+fileCols+` FROM files WHERE dev=? AND ino=? ORDER BY id DESC`, int64(dev), int64(ino))
}

// FilesByCurrentPath returns the files whose current path is path.
func (s *Store) FilesByCurrentPath(path string) ([]File, error) {
	return s.files(`SELECT `+fileCols+` FROM files WHERE id IN (SELECT file_id FROM paths WHERE path=? AND closed_ns IS NULL) ORDER BY id DESC`, path)
}

// AllFiles returns every file ordered by id.
func (s *Store) AllFiles() ([]File, error) {
	return s.files(`SELECT ` + fileCols + ` FROM files ORDER BY id`)
}

func (s *Store) files(q string, args ...any) ([]File, error) {
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CurrentPaths returns the paths that currently name the file.
func (s *Store) CurrentPaths(file int64) ([]string, error) {
	return s.strings(`SELECT path FROM paths WHERE file_id=? AND closed_ns IS NULL ORDER BY path`, file)
}

// AllCurrentPaths returns the current paths of every file in one query, each
// file's paths ordered by path.
func (s *Store) AllCurrentPaths() (map[int64][]string, error) {
	rows, err := s.rdb.Query(`SELECT file_id,path FROM paths WHERE closed_ns IS NULL ORDER BY file_id,path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}

// PathHistory returns every path the file ever had, in first-seen order.
func (s *Store) PathHistory(file int64) ([]string, error) {
	return s.strings(`SELECT path FROM paths WHERE file_id=? ORDER BY first_seen_ns, path`, file)
}

func (s *Store) strings(q string, args ...any) ([]string, error) {
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const genCols = `file_id,gen,method,reason,start_ns,copy_offset,parse_offset,tail_hash,tail_len`

func scanGen(sc interface{ Scan(...any) error }) (Generation, error) {
	var g Generation
	var th int64
	err := sc.Scan(&g.File, &g.Gen, &g.Method, &g.Reason, &g.StartNS, &g.CopyOffset, &g.ParseOffset, &th, &g.TailLen)
	g.TailHash = uint64(th)
	return g, err
}

// Generation returns one generation.
func (s *Store) Generation(file int64, gen int) (Generation, error) {
	g, err := scanGen(s.rdb.QueryRow(`SELECT `+genCols+` FROM generations WHERE file_id=? AND gen=?`, file, gen))
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrNotFound
	}
	return g, err
}

// Generations returns every generation of a file in order.
func (s *Store) Generations(file int64) ([]Generation, error) {
	rows, err := s.rdb.Query(`SELECT `+genCols+` FROM generations WHERE file_id=? ORDER BY gen`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Generation
	for rows.Next() {
		g, err := scanGen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AllGenerations returns every generation of every file.
func (s *Store) AllGenerations() ([]Generation, error) {
	rows, err := s.rdb.Query(`SELECT ` + genCols + ` FROM generations ORDER BY file_id, gen`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Generation
	for rows.Next() {
		g, err := scanGen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetParseOffset records how far the parser has parsed a generation. It is a
// derived column, not an observation, and MUST NOT exceed the copy offset.
func (s *Store) SetParseOffset(file int64, gen int, off int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.wdb.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, off, file, gen, off)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: parse offset %d beyond copy offset or unknown generation %d.%d", off, file, gen)
	}
	return nil
}

// LatestSnapshot returns the newest stored snapshot version of a file.
func (s *Store) LatestSnapshot(file int64) (Snapshot, error) {
	var sn Snapshot
	var h int64
	err := s.rdb.QueryRow(`SELECT version,hash,size,seen_ns FROM snapshots WHERE file_id=? ORDER BY version DESC LIMIT 1`, file).Scan(&sn.Version, &h, &sn.Size, &sn.SeenNS)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	sn.Hash = uint64(h)
	return sn, err
}

// Snapshots returns every stored version of a file.
func (s *Store) Snapshots(file int64) ([]Snapshot, error) {
	rows, err := s.rdb.Query(`SELECT version,hash,size,seen_ns FROM snapshots WHERE file_id=? ORDER BY version`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Snapshot
	for rows.Next() {
		var sn Snapshot
		var h int64
		if err := rows.Scan(&sn.Version, &h, &sn.Size, &sn.SeenNS); err != nil {
			return nil, err
		}
		sn.Hash = uint64(h)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Tombstones returns the deletion times of a file.
func (s *Store) Tombstones(file int64) ([]int64, error) {
	rows, err := s.rdb.Query(`SELECT at_ns FROM tombstones WHERE file_id=? ORDER BY at_ns`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Commit describes one copy commit (the time the recorder saw those bytes).
type CopyCommit struct {
	Gen      int
	Seq      int64
	From, To int64
	SeenNS   int64
}

// CopyCommits returns the per-commit seen-times of a file.
func (s *Store) CopyCommits(file int64) ([]CopyCommit, error) {
	rows, err := s.rdb.Query(`SELECT gen,seq,from_off,to_off,seen_ns FROM commits WHERE file_id=? ORDER BY seq`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CopyCommit
	for rows.Next() {
		var c CopyCommit
		if err := rows.Scan(&c.Gen, &c.Seq, &c.From, &c.To, &c.SeenNS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Resets returns the reset entries; pinnedOnly keeps those clients behind have
// not advanced past. The retention floor MUST NOT prune a pinned reset.
func (s *Store) Resets(pinnedOnly bool) ([]Reset, error) {
	q := `SELECT id,file_id,from_gen,to_gen,reason,at_ns,pinned FROM resets`
	if pinnedOnly {
		q += ` WHERE pinned=1`
	}
	rows, err := s.rdb.Query(q + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Reset
	for rows.Next() {
		var r Reset
		var p int
		if err := rows.Scan(&r.ID, &r.File, &r.FromGen, &r.ToGen, &r.Reason, &r.AtNS, &p); err != nil {
			return nil, err
		}
		r.Pinned = p == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReleaseResets unpins reset entries with id <= upTo once every client behind
// them has advanced (journaled, so a rebuild keeps the same pins).
func (s *Store) ReleaseResets(upTo int64, atNS int64) error {
	return s.Commit(&Rec{T: TRelease, At: atNS, Version: int(upTo)})
}

// StartGeneration starts a new generation of the file's copy from byte 0 (a
// full-hash mismatch at settle or in doctor, or a shrink, rewrite or tail
// mismatch in the copier). The old generation is kept and a pinned reset
// marker is recorded in the same commit as the new generation. method is the
// copy method (clone or bytes) of the new generation; the caller records the
// first commit.
func (s *Store) StartGeneration(file int64, reason, method string, atNS int64) (int, error) {
	f, err := s.File(file)
	if err != nil {
		return 0, err
	}
	gen := f.CurGen + 1
	if err := s.Commit(&Rec{T: TGen, At: atNS, File: file, Gen: gen, Method: method, Reason: reason, Reset: f.CurGen > 0}); err != nil {
		return 0, err
	}
	return gen, nil
}

// CountObservations returns row counts used by tests and status.
func (s *Store) CountObservations() (map[string]int, error) {
	out := map[string]int{}
	for _, t := range []string{"files", "paths", "generations", "commits", "snapshots", "tombstones", "resets"} {
		var n int
		if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// Dump renders every observation deterministically (parse offsets, a derived
// column, are excluded). The invariants helper compares it with ReplayDump.
func (s *Store) Dump() (string, error) {
	var b strings.Builder
	tables := []struct{ name, q string }{
		{"meta", `SELECT key||'='||value FROM meta WHERE key IN ('epoch','schema_version') ORDER BY key`},
		{"files", `SELECT id||'|'||source||'|'||mode||'|'||dev||'|'||ino||'|'||birth_ns||'|'||first_hash||'|'||link_target||'|'||state||'|'||first_seen_ns||'|'||last_size||'|'||cur_gen||'|'||deleted_ns FROM files ORDER BY id`},
		{"paths", `SELECT file_id||'|'||path||'|'||first_seen_ns||'|'||COALESCE(closed_ns,'') FROM paths ORDER BY file_id,path,first_seen_ns`},
		{"generations", `SELECT file_id||'|'||gen||'|'||method||'|'||reason||'|'||start_ns||'|'||copy_offset||'|'||tail_hash||'|'||tail_len FROM generations ORDER BY file_id,gen`},
		{"commits", `SELECT file_id||'|'||gen||'|'||seq||'|'||from_off||'|'||to_off||'|'||seen_ns FROM commits ORDER BY seq`},
		{"snapshots", `SELECT file_id||'|'||version||'|'||hash||'|'||size||'|'||seen_ns FROM snapshots ORDER BY file_id,version`},
		{"tombstones", `SELECT file_id||'|'||path||'|'||at_ns FROM tombstones ORDER BY file_id,at_ns`},
		{"resets", `SELECT id||'|'||file_id||'|'||from_gen||'|'||to_gen||'|'||reason||'|'||at_ns||'|'||pinned FROM resets ORDER BY id`},
	}
	for _, t := range tables {
		rows, err := s.rdb.Query(t.q)
		if err != nil {
			return "", err
		}
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				_ = rows.Close()
				return "", err
			}
			lines = append(lines, l)
		}
		_ = rows.Close()
		sort.Strings(lines)
		b.WriteString("[" + t.name + "]\n" + strings.Join(lines, "\n") + "\n")
	}
	return b.String(), nil
}

// ReplayDump replays the journal at journalPath into a scratch state directory
// and returns its Dump. The scratch directory is removed.
func ReplayDump(journalPath string) (string, error) {
	dir, err := os.MkdirTemp("", "ccrec-replay-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	data, err := os.ReadFile(journalPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dir+"/"+JournalName, data, 0o600); err != nil {
		return "", err
	}
	st, err := Open(dir, Options{ReplayOnly: true})
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()
	return st.Dump()
}

// SnapshotFileByPathHistory returns the newest snapshot file that ever had
// path (a snapshot source is keyed by path, so a deleted path that comes back
// continues the same file).
func (s *Store) SnapshotFileByPathHistory(path string) (File, error) {
	f, err := scanFile(s.rdb.QueryRow(`SELECT `+fileCols+` FROM files WHERE mode='snapshot' AND id IN (SELECT file_id FROM paths WHERE path=?) ORDER BY id DESC LIMIT 1`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	return f, err
}

// AllPaths returns, for every file, its current paths and (when it has none)
// every path it ever had, in one query pair; the settle stage groups files into
// Conversations with it.
func (s *Store) AllPaths() (map[int64][]string, error) {
	out := map[int64][]string{}
	rows, err := s.rdb.Query(`SELECT file_id, path, closed_ns IS NULL FROM paths ORDER BY file_id, first_seen_ns, path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cur := map[int64][]string{}
	hist := map[int64][]string{}
	for rows.Next() {
		var id int64
		var p string
		var open bool
		if err := rows.Scan(&id, &p, &open); err != nil {
			return nil, err
		}
		if open {
			cur[id] = append(cur[id], p)
		}
		hist[id] = append(hist[id], p)
	}
	for id, h := range hist {
		if c := cur[id]; len(c) > 0 {
			out[id] = c
		} else {
			out[id] = h
		}
	}
	return out, rows.Err()
}
