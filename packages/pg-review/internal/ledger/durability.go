package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// IntegrityCheck runs PRAGMA integrity_check and returns ErrIntegrity (with
// the first reported problems) unless the result is a single "ok".
func (l *Ledger) IntegrityCheck(ctx context.Context) error {
	rows, err := l.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	defer func() { _ = rows.Close() }()
	var msgs []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return fmt.Errorf("%w: %v", ErrIntegrity, err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	if len(msgs) == 1 && msgs[0] == "ok" {
		return nil
	}
	if len(msgs) > 5 {
		msgs = msgs[:5]
	}
	return fmt.Errorf("%w: %s", ErrIntegrity, strings.Join(msgs, "; "))
}

// Export writes the whole ledger as JSONL: a header line
// {"export":"pg-review-ledger","schema_version":N}, then one
// {"table":T,"row":{...}} line per row, tables in name order, rows in rowid
// order. It reads one consistent snapshot.
func (l *Ledger) Export(ctx context.Context, w io.Writer) error {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	// Detached: the rollback must run even when ctx is already cancelled.
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()
	var ver int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(map[string]any{"export": "pg-review-ledger", "schema_version": ver}); err != nil {
		return err
	}
	trows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for trows.Next() {
		var n string
		if err := trows.Scan(&n); err != nil {
			_ = trows.Close()
			return err
		}
		tables = append(tables, n)
	}
	if err := trows.Close(); err != nil {
		return err
	}
	for _, tb := range tables {
		if err := exportTable(ctx, conn, enc, tb); err != nil {
			return err
		}
	}
	return nil
}

func exportTable(ctx context.Context, conn *sql.Conn, enc *json.Encoder, table string) error {
	rows, err := conn.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		if err := enc.Encode(map[string]any{"table": table, "row": row}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ExportFile writes the JSONL export to dest atomically (temp file, fsync,
// rename) with mode 0600.
func (l *Ledger) ExportFile(ctx context.Context, dest string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".export-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := l.Export(ctx, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// Backup writes a consistent SQLite copy of the ledger to dest (which must not
// exist) with mode 0600, using VACUUM INTO.
func (l *Ledger) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%w: backup destination %s exists", ErrInvalid, dest)
	}
	if _, err := l.db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}

// RulingsLogPath is the append-only rulings log next to the database.
func (l *Ledger) RulingsLogPath() string {
	return filepath.Join(filepath.Dir(l.path), "rulings.jsonl")
}

// RulingLogEntry is one line of the rulings log.
type RulingLogEntry struct {
	At             string `json:"at"`
	TopicID        string `json:"topic_id"`
	Lens           string `json:"lens,omitempty"`
	Ruling         string `json:"ruling"`
	Rationale      string `json:"rationale,omitempty"`
	ReconsiderWhen string `json:"reconsider_when,omitempty"`
}

// AppendRulingLog appends one entry to the rulings log (O_APPEND, fsynced,
// mode 0600). The log is the fallback record if the database is lost; it is
// never rewritten.
func (l *Ledger) AppendRulingLog(e RulingLogEntry) error {
	if e.At == "" {
		e.At = fmtTime(l.now())
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.RulingsLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
