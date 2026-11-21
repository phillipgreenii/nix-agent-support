// Package store is work-report's SQLite-backed history store: the durable,
// append-only record of every activity entry work-report has observed, plus
// the pull log (design 7.4). Nothing about reports is stored.
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
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
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

// ErrLocked is returned (wrapped; test with errors.Is) by Open, Append and
// RecordPull when SQLite stays busy past its busy timeout.
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
}

// Store is an open work-report store.
type Store struct {
	sql  *sql.DB
	path string
}

// Option adjusts Open. It exists so a caller (a test of lock handling) can
// shorten the busy timeout without waiting out the production five seconds.
type Option func(busyMillis *int)

// WithBusyTimeout sets the SQLite busy_timeout Open applies.
func WithBusyTimeout(d time.Duration) Option {
	return func(busyMillis *int) { *busyMillis = int(d.Milliseconds()) }
}

// Open opens (creating if absent, with its parent directories) the SQLite
// database at path, enables WAL, sets a busy timeout and applies the
// migration ladder. A busy database surfaces as ErrLocked.
func Open(path string, opts ...Option) (*Store, error) {
	busy := busyTimeoutMillis
	for _, o := range opts {
		o(&busy)
	}
	// Opened through the shared sqliteopen helper (standard pragma set, one
	// connection to serialize this process's writers; WAL + busy timeout handle
	// other processes). TxLockImmediate makes every transaction take the write
	// lock at BEGIN, so the busy timeout (not a mid-transaction SQLITE_BUSY)
	// governs waits.
	o := sqliteopen.Standard(path)
	o.TxLockImmediate = true
	o.BusyTimeoutMillis = busy
	db, err := sqliteopen.Open(o)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, mapErr(err))
	}
	s := &Store{sql: db, path: path}
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
	return s.append(ctx, e, now, false)
}

// AppendKeepEnriched is Append for an entry observed from a truncated pull,
// where the source may have returned the entry without its enrichment (the
// "branch:" labels and the insertions and deletions fields). When the latest
// stored entry for e.ID differs from e ONLY by carrying that enrichment, the
// stored entry is kept and Unchanged is returned; any other difference, and a
// first sighting, is appended exactly as Append would.
func (s *Store) AppendKeepEnriched(ctx context.Context, e Entry, now time.Time) (AppendResult, error) {
	return s.append(ctx, e, now, true)
}

// enrichmentFields are the top-level Fields keys a source may omit when its
// enrichment ran out of time.
var enrichmentFields = []string{"insertions", "deletions"}

// enrichmentLabelPrefix marks the labels that are enrichment.
const enrichmentLabelPrefix = "branch:"

// stripEnrichment returns the stored labels and fields without enrichment.
func stripEnrichment(labels []string, fields string) ([]string, json.RawMessage, error) {
	kept := make([]string, 0, len(labels))
	for _, l := range labels {
		if !strings.HasPrefix(l, enrichmentLabelPrefix) {
			kept = append(kept, l)
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fields), &m); err != nil {
		// not an object: nothing to strip.
		return kept, json.RawMessage(fields), nil
	}
	for _, k := range enrichmentFields {
		delete(m, k)
	}
	b, err := json.Marshal(m)
	return kept, b, err
}

// enrichmentOnly reports whether e (hashed with labels) equals the stored
// latest row once the stored row's enrichment is removed. A stored row with no
// enrichment never matches, because then it would differ from e in the very
// way that made e a new version.
func enrichmentOnly(e Entry, labels, storedLabels []string, storedFields, storedType, storedSummary, storedURL, storedOccurred string) (bool, error) {
	kl, kf, err := stripEnrichment(storedLabels, storedFields)
	if err != nil {
		return false, err
	}
	twin := Entry{
		Type: storedType, OccurredAt: e.OccurredAt, Summary: storedSummary,
		URL: storedURL, Fields: kf,
	}
	if storedOccurred != formatTime(e.OccurredAt) {
		return false, nil
	}
	want, err := contentHash(twin, normalizeLabels(kl))
	if err != nil {
		return false, err
	}
	got, err := contentHash(e, labels)
	if err != nil {
		return false, err
	}
	return want == got, nil
}

func (s *Store) append(ctx context.Context, e Entry, now time.Time, keepEnriched bool) (AppendResult, error) {
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
	case err == nil && keepEnriched:
		var typ, occurred, summary, url, lj, fj string
		if err := tx.QueryRowContext(
			ctx,
			`SELECT type, occurred_at, summary, url, labels, fields FROM entry_latest WHERE id = ?`, e.ID,
		).Scan(&typ, &occurred, &summary, &url, &lj, &fj); err != nil {
			return Appended, fmt.Errorf("store: append %s: %w", e.ID, mapErr(err))
		}
		var stored []string
		if err := json.Unmarshal([]byte(lj), &stored); err != nil {
			return Appended, fmt.Errorf("store: append %s: stored labels: %w", e.ID, err)
		}
		if typ == e.Type {
			same, err := enrichmentOnly(e, labels, stored, fj, typ, summary, url, occurred)
			if err != nil {
				return Appended, fmt.Errorf("store: append %s: %w", e.ID, err)
			}
			if same {
				return Unchanged, nil
			}
		}
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
