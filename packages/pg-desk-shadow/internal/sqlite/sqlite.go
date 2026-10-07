// Package sqlite is a thin wrapper over the sqlite3 command-line tool: the
// module stays stdlib-only (no cgo driver) and every store access is an
// explicit, auditable child process.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Bin is the sqlite3 executable name; tests and the collector may override it.
var Bin = "sqlite3"

// DB addresses one database file.
type DB struct {
	Path string
	// ReadOnly opens the file with -readonly. It is only usable on a database
	// whose -shm/-wal files already exist or are not needed (the LIVE store);
	// a freshly copied WAL database cannot be opened read-only.
	ReadOnly bool
}

func (d DB) args(extra ...string) []string {
	a := []string{}
	if d.ReadOnly {
		a = append(a, "-readonly")
	}
	a = append(a, "-bail", "-cmd", ".timeout 15000")
	a = append(a, extra...)
	a = append(a, d.Path)
	return a
}

func run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, Bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Exec runs a SQL script (several statements allowed) and discards its output.
func (d DB) Exec(ctx context.Context, script string) error {
	_, err := run(ctx, d.args(), script)
	return err
}

// Query runs one SELECT and decodes the rows (numbers stay json.Number).
func (d DB) Query(ctx context.Context, sql string) ([]map[string]any, error) {
	out, err := run(ctx, d.args("-json"), sql)
	if err != nil {
		return nil, err
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("sqlite3: decode rows: %w", err)
	}
	return rows, nil
}

// Int runs a query that returns one integer in one row.
func (d DB) Int(ctx context.Context, sql string) (int64, error) {
	rows, err := d.Query(ctx, sql)
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, fmt.Errorf("sqlite3: expected one value from %q", sql)
	}
	for _, v := range rows[0] {
		switch n := v.(type) {
		case json.Number:
			return n.Int64()
		case nil:
			return 0, nil
		}
	}
	return 0, fmt.Errorf("sqlite3: non-integer result from %q", sql)
}

// Backup writes a consistent snapshot of src (opened read-only) to dst using
// the SQLite backup API, which is safe against a live WAL database.
func Backup(ctx context.Context, src, dst string) error {
	_, err := run(ctx, []string{"-readonly", "-bail", "-cmd", ".timeout 15000", src, ".backup '" + strings.ReplaceAll(dst, "'", "''") + "'"}, "")
	return err
}

// Quote returns s as a SQL string literal.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Str reads a string column value from a Query row.
func Str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	}
	return ""
}

// Num reads an integer column value from a Query row.
func Num(v any) int64 {
	if n, ok := v.(json.Number); ok {
		i, _ := n.Int64()
		return i
	}
	return 0
}
