package store

import (
	"context"
	"database/sql"
	"errors"
)

// Proc is a row of the procs table: what the recorder observed of one Process
// (pid plus start time) that no file records.
type Proc struct {
	Pid         int64
	ProcStart   string
	FirstSeenNS int64
	LastSeenNS  int64
	// ExitNS is the instant the exit was observed (ExitKind observed) or the
	// time the recorder found the Process already gone (unobserved); 0 while the
	// Process lives.
	ExitNS   int64
	ExitKind string // "", observed or unobserved
	// RegistryPresent is whether the registry file was still present at the
	// instant of an observed exit.
	RegistryPresent bool
	// Stale is registry_stale_at_discovery: at first discovery the registry
	// file named a dead pid (gone, or a different start time).
	Stale bool
	Host  string // the host binding JSON, "" when not read
}

// Exit kinds of a Proc.
const (
	ExitObserved   = "observed"
	ExitUnobserved = "unobserved"
)

func applyProc(ctx context.Context, tx *sql.Tx, r *Rec) error {
	// Every kind starts by making sure the row exists, so replay in any order
	// converges.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO procs(pid,proc_start,first_seen_ns) VALUES(?,?,?)`, r.Pid, r.ProcStart, r.At); err != nil {
		return err
	}
	var err error
	switch r.Kind {
	case "discover":
		_, err = tx.ExecContext(ctx, `UPDATE procs SET host=?, stale=? WHERE pid=? AND proc_start=? AND first_seen_ns=?`,
			r.Val, nz(r.Reason == "stale"), r.Pid, r.ProcStart, r.At)
	case "seen":
		_, err = tx.ExecContext(ctx, `UPDATE procs SET last_seen_ns=MAX(last_seen_ns,?) WHERE pid=? AND proc_start=?`, r.At, r.Pid, r.ProcStart)
	case "exit":
		_, err = tx.ExecContext(ctx, `UPDATE procs SET exit_ns=?, exit_kind=?, registry_present=? WHERE pid=? AND proc_start=? AND exit_ns=0`,
			r.At, r.Reason, nz(r.Val == "present"), r.Pid, r.ProcStart)
	}
	return err
}

const procCols = `pid,proc_start,first_seen_ns,last_seen_ns,exit_ns,exit_kind,registry_present,stale,host`

func scanProc(sc interface{ Scan(...any) error }) (Proc, error) {
	var p Proc
	var present, stale int
	err := sc.Scan(&p.Pid, &p.ProcStart, &p.FirstSeenNS, &p.LastSeenNS, &p.ExitNS, &p.ExitKind, &present, &stale, &p.Host)
	p.RegistryPresent, p.Stale = present != 0, stale != 0
	return p, err
}

// Procs returns every observed Process, oldest first.
func (s *Reader) Procs() ([]Proc, error) {
	rows, err := s.q.Query(`SELECT ` + procCols + ` FROM procs ORDER BY first_seen_ns, pid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Proc
	for rows.Next() {
		p, err := scanProc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Proc returns the Process with the given pid and start time.
func (s *Reader) Proc(pid int64, procStart string) (Proc, error) {
	p, err := scanProc(s.q.QueryRow(`SELECT `+procCols+` FROM procs WHERE pid=? AND proc_start=?`, pid, procStart))
	if errors.Is(err, sql.ErrNoRows) {
		return Proc{}, ErrNotFound
	}
	return p, err
}

// ProcRec builds a Process observation record.
func ProcRec(kind string, pid int64, procStart string, atNS int64) *Rec {
	return &Rec{T: TProc, Kind: kind, Pid: pid, ProcStart: procStart, At: atNS}
}
