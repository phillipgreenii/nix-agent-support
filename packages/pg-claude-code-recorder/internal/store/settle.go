package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// RangeRec is one byte range of one generation that an archive segment holds.
type RangeRec struct {
	File int64  `json:"file"`
	Gen  int    `json:"gen"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
	Seg  string `json:"seg"` // segment file name inside the session's archive directory
}

// SettleRec is the Val of a TSettle record: the manifest (JSON) and the ranges
// the archive segment written for this settle covers (none in clone mode).
type SettleRec struct {
	Manifest json.RawMessage `json:"manifest"`
	Ranges   []RangeRec      `json:"ranges,omitempty"`
}

// Stages persisted in conv_stage (live and quiet are derived, never stored).
const (
	StageSettled = "settled"
	StageHeld    = "held"
	// StageRemovalPending and StageRemoved are the removal stages; StageExtracting
	// marks an extract whose files are not all restored (its Val lists them).
	StageRemovalPending = "removal_pending"
	StageRemoved        = "removed"
	StageExtracting     = "extracting"
)

func applySettle(tx *sql.Tx, r *Rec) error {
	switch r.T {
	case TStage:
		if r.State == "" {
			_, err := tx.Exec(`DELETE FROM conv_stage WHERE session=?`, r.Session)
			return err
		}
		_, err := tx.Exec(`INSERT INTO conv_stage(session,stage,reason,since_ns,detail) VALUES(?,?,?,?,?)
ON CONFLICT(session) DO UPDATE SET stage=excluded.stage, reason=excluded.reason, since_ns=excluded.since_ns, detail=excluded.detail`,
			r.Session, r.State, r.Reason, r.At, r.Val)
		return err
	case TSettle:
		var sr SettleRec
		if err := json.Unmarshal([]byte(r.Val), &sr); err != nil {
			return fmt.Errorf("settle record: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO settled(session,seq,at_ns,method,manifest) VALUES(?,1,?,?,?)
ON CONFLICT(session) DO UPDATE SET seq=seq+1, at_ns=excluded.at_ns, method=excluded.method, manifest=excluded.manifest`,
			r.Session, r.At, r.Method, string(sr.Manifest)); err != nil {
			return err
		}
		for _, g := range sr.Ranges {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO ranges(file_id,gen,from_off,to_off,session,seg) VALUES(?,?,?,?,?,?)`,
				g.File, g.Gen, g.From, g.To, r.Session, g.Seg); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO conv_stage(session,stage,reason,since_ns,detail) VALUES(?,?,?,?,?)
ON CONFLICT(session) DO UPDATE SET stage=excluded.stage, reason=excluded.reason, since_ns=excluded.since_ns, detail=excluded.detail`,
			r.Session, StageSettled, "", r.At, "")
		return err
	case TTorn:
		if r.Reset {
			_, err := tx.Exec(`DELETE FROM torn_tails WHERE file_id=? AND gen=?`, r.File, r.Gen)
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO torn_tails(file_id,gen,off,len,at_ns) VALUES(?,?,?,?,?)`,
			r.File, r.Gen, r.From, r.To-r.From, r.At)
		return err
	}
	return nil
}

// StageRow is a row of conv_stage.
type StageRow struct {
	Session string
	Stage   string
	Reason  string
	SinceNS int64
	Detail  string
}

// Stages returns every persisted stage (settled and held Conversations).
func (s *Store) Stages() ([]StageRow, error) {
	rows, err := s.rdb.Query(`SELECT session,stage,reason,since_ns,detail FROM conv_stage ORDER BY session`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StageRow
	for rows.Next() {
		var x StageRow
		if err := rows.Scan(&x.Session, &x.Stage, &x.Reason, &x.SinceNS, &x.Detail); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Stage returns the persisted stage of a session ("" when none).
func (s *Store) Stage(session string) (StageRow, bool, error) {
	var x StageRow
	err := s.rdb.QueryRow(`SELECT session,stage,reason,since_ns,detail FROM conv_stage WHERE session=?`, session).
		Scan(&x.Session, &x.Stage, &x.Reason, &x.SinceNS, &x.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return StageRow{}, false, nil
	}
	return x, err == nil, err
}

// Settled is a row of the settled table.
type Settled struct {
	Session  string
	Seq      int
	AtNS     int64
	Method   string
	Manifest []byte
}

// SettledOf returns the settle record of a session.
func (s *Store) SettledOf(session string) (Settled, bool, error) {
	var x Settled
	var m string
	err := s.rdb.QueryRow(`SELECT session,seq,at_ns,method,manifest FROM settled WHERE session=?`, session).
		Scan(&x.Session, &x.Seq, &x.AtNS, &x.Method, &m)
	if errors.Is(err, sql.ErrNoRows) {
		return Settled{}, false, nil
	}
	x.Manifest = []byte(m)
	return x, err == nil, err
}

// Range is a row of the ranges table.
type Range struct {
	File     int64
	Gen      int
	From, To int64
	Session  string
	Seg      string
}

// Ranges returns the archived ranges of one generation, ordered by offset.
func (s *Store) Ranges(file int64, gen int) ([]Range, error) {
	return s.ranges(`SELECT file_id,gen,from_off,to_off,session,seg FROM ranges WHERE file_id=? AND gen=? ORDER BY from_off`, file, gen)
}

// AllRanges returns every archived range.
func (s *Store) AllRanges() ([]Range, error) {
	return s.ranges(`SELECT file_id,gen,from_off,to_off,session,seg FROM ranges ORDER BY file_id,gen,from_off`)
}

func (s *Store) ranges(q string, args ...any) ([]Range, error) {
	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Range
	for rows.Next() {
		var x Range
		if err := rows.Scan(&x.File, &x.Gen, &x.From, &x.To, &x.Session, &x.Seg); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ArchivedTo returns the end of the archived prefix [0, n) of a generation
// (0 when nothing is archived): the bytes before it are in an archive, not in
// the copy file.
func (s *Store) ArchivedTo(file int64, gen int) int64 {
	rs, err := s.Ranges(file, gen)
	if err != nil {
		return 0
	}
	var n int64
	for _, r := range rs {
		if r.From > n {
			break
		}
		if r.To > n {
			n = r.To
		}
	}
	return n
}

// TornTail is a torn-tail fact.
type TornTail struct {
	File    int64
	Gen     int
	Off     int64
	Len     int64
	AtNS    int64
	Session string
}

// TornTails returns every recorded torn-tail fact.
func (s *Store) TornTails() ([]TornTail, error) {
	rows, err := s.rdb.Query(`SELECT file_id,gen,off,len,at_ns FROM torn_tails ORDER BY file_id,gen`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TornTail
	for rows.Next() {
		var x TornTail
		if err := rows.Scan(&x.File, &x.Gen, &x.Off, &x.Len, &x.AtNS); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// TornTailOf returns the torn-tail fact of a generation.
func (s *Store) TornTailOf(file int64, gen int) (TornTail, bool) {
	var x TornTail
	err := s.rdb.QueryRow(`SELECT file_id,gen,off,len,at_ns FROM torn_tails WHERE file_id=? AND gen=?`, file, gen).
		Scan(&x.File, &x.Gen, &x.Off, &x.Len, &x.AtNS)
	return x, err == nil
}

// SessionOfFile is the sorted distinct session ids the records of a file carry.
func (s *Store) SessionsOfFile(file int64) ([]string, error) {
	rows, err := s.rdb.Query(`SELECT DISTINCT session FROM records WHERE file_id=? AND session<>''`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	sort.Strings(out)
	return out, rows.Err()
}
