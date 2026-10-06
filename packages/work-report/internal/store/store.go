// Package store is work-report's SQLite-backed history store: the durable,
// append-only record of every activity entry work-report has observed, plus
// the pull and report logs (design 7.4).
//
// Nothing written here is ever mutated or deleted (INV-APPEND-1): entries are
// only ever INSERTed, and every read of entries goes through the latest-wins
// view (INV-LATEST-1, the entry_latest view in migrations.go).
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// busyTimeoutMillis is the SQLite busy_timeout applied on every connection.
// It is a variable only so this package's own tests can shorten it.
var busyTimeoutMillis = 5000

// SQLite primary result codes that mean "another connection holds the lock".
const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

// ErrLocked is returned (wrapped; test with errors.Is) by Open, Append,
// RecordPull and RecordReport when SQLite stays busy past its busy timeout.
var ErrLocked = errors.New("work-report store: locked")

// timeLayout is a fixed-width UTC layout, so stored timestamps sort
// lexicographically in chronological order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// Entry is one stored fact.
type Entry struct {
	Seq                    int64
	ID, ExternalID         string
	SourceID, Type         string
	OccurredAt, IngestedAt time.Time // UTC
	Summary, URL           string
	Labels                 []string
	Fields                 json.RawMessage
	ContentHash            string
}

// AppendResult reports what Append did.
type AppendResult int

const (
	// Appended means a new entry row was written.
	Appended AppendResult = iota
	// Unchanged means the latest entry for the id already had an identical
	// content hash; nothing was written.
	Unchanged
)

// PullRow is one per-source pull outcome.
type PullRow struct {
	Source                     string
	Since, Before              time.Time // Since is the zero time for an open-ended range
	StartedAt, EndedAt         time.Time
	Status                     string // "succeeded" | "degraded" | "disabled"
	Count, Unchanged, Rejected int
	Truncated                  bool
	Reason                     string
}

// ReportRow is one rendered report.
type ReportRow struct {
	Since, Before       time.Time
	Kind, NarrowingJSON string
	GeneratedAt         time.Time
	Generator, Content  string
}

// Filter selects entries. Fields combine with AND; the values within one list
// field combine with OR (WT-D11).
type Filter struct {
	Since, Before          time.Time
	OpenStart              bool
	ID                     string
	Types, Labels, Sources []string
}

// SourceStatus is the per-source health line of Status.
type SourceStatus struct {
	Source, LastStatus, LastReason          string
	LastAt, LastSuccessAt, NewestOccurredAt time.Time // zero time = none yet
	EntryCount                              int
}

// StatusData is what Status returns.
type StatusData struct {
	Sources        []SourceStatus
	StoreSizeBytes int64
	LastReport     *ReportRow // nil when no report was ever stored
}

// Store is an open work-report store.
type Store struct {
	sql  *sql.DB
	path string
}

// Open opens (creating if absent, with its parent directories) the SQLite
// database at path, enables WAL, sets a busy timeout and applies the
// migration ladder. A busy database surfaces as ErrLocked.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir for %s: %w", path, err)
	}
	// _txlock=immediate makes every transaction take the write lock at BEGIN,
	// so the busy timeout (not a mid-transaction SQLITE_BUSY) governs waits.
	dsn := fmt.Sprintf("file:%s?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)",
		path, busyTimeoutMillis)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection serializes this process's writers; WAL + busy timeout
	// handle other processes.
	db.SetMaxOpenConns(1)
	s := &Store{sql: db, path: path}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, mapErr(err))
	}
	if err := migrate(s); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, mapErr(err))
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.sql.Close() }

// mapErr wraps SQLite busy/locked errors with ErrLocked.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		if c := se.Code() & 0xff; c == sqliteBusy || c == sqliteLocked {
			return fmt.Errorf("%w: %v", ErrLocked, err)
		}
	}
	return err
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// optTime renders a possibly-zero time; the zero time is stored as "".
func optTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatTime(t)
}

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeLayout, v)
}

// normalizeLabels sorts and de-duplicates labels.
func normalizeLabels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, l := range in {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// volatileFields are the top-level Fields keys excluded from the content hash:
// a re-observation differing only in them is not a new fact (design 7.3).
var volatileFields = []string{"as_of", "stale"}

// canonicalFields returns Fields with the volatile keys removed and the JSON
// canonicalized (keys sorted; encoding/json sorts map keys). Empty Fields
// canonicalize to "{}".
func canonicalFields(raw json.RawMessage) ([]byte, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("fields is not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("fields has trailing data")
	}
	if m, ok := v.(map[string]any); ok {
		for _, k := range volatileFields {
			delete(m, k)
		}
	}
	return json.Marshal(v)
}

// contentHash hashes type, occurred_at, labels (already normalized), summary,
// url and fields (volatile keys removed, canonicalized).
func contentHash(e Entry, labels []string) (string, error) {
	fields, err := canonicalFields(e.Fields)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Type       string          `json:"type"`
		OccurredAt string          `json:"occurred_at"`
		Labels     []string        `json:"labels"`
		Summary    string          `json:"summary"`
		URL        string          `json:"url"`
		Fields     json.RawMessage `json:"fields"`
	}{e.Type, formatTime(e.OccurredAt), labels, e.Summary, e.URL, fields})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// Append stores e unless the latest entry for e.ID already has an identical
// content hash, in which case nothing is written and Unchanged is returned.
// The hash is computed here (e.ContentHash is ignored); IngestedAt is set to
// now. Labels are sorted and de-duplicated before hashing and storing.
func (s *Store) Append(ctx context.Context, e Entry, now time.Time) (AppendResult, error) {
	if e.ID == "" {
		return Appended, errors.New("store: append: entry id is empty")
	}
	labels := normalizeLabels(e.Labels)
	hash, err := contentHash(e, labels)
	if err != nil {
		return Appended, fmt.Errorf("store: append %s: %w", e.ID, err)
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Appended, err
	}
	fields := json.RawMessage("{}")
	if len(strings.TrimSpace(string(e.Fields))) > 0 {
		fields = e.Fields
	}

	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
	}
	defer func() { _ = tx.Rollback() }()

	var latest string
	err = tx.QueryRowContext(ctx, `SELECT content_hash FROM entry_latest WHERE id = ?`, e.ID).Scan(&latest)
	switch {
	case err == nil && latest == hash:
		return Unchanged, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO entry (id, external_id, source_id, type, occurred_at, ingested_at, summary, url, labels, fields, content_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.ExternalID, e.SourceID, e.Type, formatTime(e.OccurredAt), formatTime(now),
		e.Summary, e.URL, string(labelsJSON), string(fields), hash)
	if err != nil {
		return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Appended, err
	}
	for _, l := range labels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO entry_label (seq, label) VALUES (?, ?)`, seq, l); err != nil {
			return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
		}
	}
	if err := tx.Commit(); err != nil {
		return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
	}
	return Appended, nil
}

func setMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// RecordPull appends a pull row and updates meta's last_pull.
func (s *Store) RecordPull(ctx context.Context, r PullRow) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: record pull: %w", mapErr(err))
	}
	defer func() { _ = tx.Rollback() }()
	trunc := 0
	if r.Truncated {
		trunc = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pull (source, since, before, started_at, ended_at, status, count, unchanged, rejected, truncated, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Source, optTime(r.Since), optTime(r.Before), formatTime(r.StartedAt), formatTime(r.EndedAt),
		r.Status, r.Count, r.Unchanged, r.Rejected, trunc, r.Reason); err != nil {
		return fmt.Errorf("store: record pull: %w", mapErr(err))
	}
	if err := setMeta(ctx, tx, "last_pull", formatTime(r.EndedAt)); err != nil {
		return fmt.Errorf("store: record pull: %w", mapErr(err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: record pull: %w", mapErr(err))
	}
	return nil
}

// RecordReport appends a report row and updates meta's last_report.
func (s *Store) RecordReport(ctx context.Context, r ReportRow) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: record report: %w", mapErr(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report (since, before, kind, narrowing, generated_at, generator, content)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		optTime(r.Since), optTime(r.Before), r.Kind, r.NarrowingJSON, formatTime(r.GeneratedAt),
		r.Generator, r.Content); err != nil {
		return fmt.Errorf("store: record report: %w", mapErr(err))
	}
	if err := setMeta(ctx, tx, "last_report", formatTime(r.GeneratedAt)); err != nil {
		return fmt.Errorf("store: record report: %w", mapErr(err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: record report: %w", mapErr(err))
	}
	return nil
}
