package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Query returns the latest-wins entries (one per id) matching f, ordered by
// (occurred_at, seq). Filter fields combine with AND; values within one list
// field combine with OR. Since is inclusive and Before exclusive; a zero
// Since (or OpenStart) leaves the start open, a zero Before leaves the end
// open.
func (s *Store) Query(ctx context.Context, f Filter) ([]Entry, error) {
	var (
		where []string
		args  []any
	)
	if !f.OpenStart && !f.Since.IsZero() {
		where = append(where, "e.occurred_at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if !f.Before.IsZero() {
		where = append(where, "e.occurred_at < ?")
		args = append(args, formatTime(f.Before))
	}
	if f.ID != "" {
		where = append(where, "e.id = ?")
		args = append(args, f.ID)
	}
	if len(f.Types) > 0 {
		where = append(where, "e.type IN ("+placeholders(len(f.Types))+")")
		args = append(args, strs(f.Types)...)
	}
	if len(f.Sources) > 0 {
		where = append(where, "e.source_id IN ("+placeholders(len(f.Sources))+")")
		args = append(args, strs(f.Sources)...)
	}
	if len(f.Labels) > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM entry_label l WHERE l.seq = e.seq AND l.label IN ("+
			placeholders(len(f.Labels))+"))")
		args = append(args, strs(f.Labels)...)
	}
	q := `SELECT e.seq, e.id, e.external_id, e.source_id, e.type, e.occurred_at, e.ingested_at,
	             e.summary, e.url, e.labels, e.fields, e.content_hash
	      FROM entry_latest e`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY e.occurred_at, e.seq"

	rows, err := s.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Entry
	for rows.Next() {
		var (
			e                      Entry
			occurred, ingested     string
			labelsJSON, fieldsJSON string
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.ExternalID, &e.SourceID, &e.Type, &occurred, &ingested,
			&e.Summary, &e.URL, &labelsJSON, &fieldsJSON, &e.ContentHash); err != nil {
			return nil, fmt.Errorf("store: query: %w", err)
		}
		if e.OccurredAt, err = parseTime(occurred); err != nil {
			return nil, fmt.Errorf("store: query: occurred_at %q: %w", occurred, err)
		}
		if e.IngestedAt, err = parseTime(ingested); err != nil {
			return nil, fmt.Errorf("store: query: ingested_at %q: %w", ingested, err)
		}
		if err := json.Unmarshal([]byte(labelsJSON), &e.Labels); err != nil {
			return nil, fmt.Errorf("store: query: labels of %s: %w", e.ID, err)
		}
		e.Fields = json.RawMessage(fieldsJSON)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query: %w", err)
	}
	return out, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func strs(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// Status reports, per source, the latest pull row (LastStatus, LastReason,
// LastAt), the time of the latest succeeded pull (LastSuccessAt), the number
// of latest-wins entries and the newest occurred_at among them; plus the store
// file size and the latest report row. A source appears if it has a pull row
// or any entry (an entry's source_id is the backend name a pull is recorded
// under). Sources are ordered by name.
func (s *Store) Status(ctx context.Context) (StatusData, error) {
	var data StatusData
	by := map[string]*SourceStatus{}
	get := func(name string) *SourceStatus {
		ss, ok := by[name]
		if !ok {
			ss = &SourceStatus{Source: name}
			by[name] = ss
		}
		return ss
	}

	// Latest pull per source: greatest (ended_at, id).
	prows, err := s.sql.QueryContext(ctx, `
		SELECT p.source, p.status, p.reason, p.ended_at FROM pull p
		WHERE NOT EXISTS (SELECT 1 FROM pull n WHERE n.source = p.source
		                  AND (n.ended_at, n.id) > (p.ended_at, p.id))`)
	if err != nil {
		return data, fmt.Errorf("store: status: %w", err)
	}
	for prows.Next() {
		var source, status, reason, ended string
		if err := prows.Scan(&source, &status, &reason, &ended); err != nil {
			_ = prows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
		ss := get(source)
		ss.LastStatus, ss.LastReason = status, reason
		if ss.LastAt, err = parseTime(ended); err != nil {
			_ = prows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
	}
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return data, fmt.Errorf("store: status: %w", err)
	}
	_ = prows.Close()

	srows, err := s.sql.QueryContext(ctx,
		`SELECT source, MAX(ended_at) FROM pull WHERE status = 'succeeded' GROUP BY source`)
	if err != nil {
		return data, fmt.Errorf("store: status: %w", err)
	}
	for srows.Next() {
		var source, ended string
		if err := srows.Scan(&source, &ended); err != nil {
			_ = srows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
		if get(source).LastSuccessAt, err = parseTime(ended); err != nil {
			_ = srows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
	}
	if err := srows.Err(); err != nil {
		_ = srows.Close()
		return data, fmt.Errorf("store: status: %w", err)
	}
	_ = srows.Close()

	erows, err := s.sql.QueryContext(ctx,
		`SELECT source_id, COUNT(*), MAX(occurred_at) FROM entry_latest GROUP BY source_id`)
	if err != nil {
		return data, fmt.Errorf("store: status: %w", err)
	}
	for erows.Next() {
		var source, newest string
		var n int
		if err := erows.Scan(&source, &n, &newest); err != nil {
			_ = erows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
		ss := get(source)
		ss.EntryCount = n
		if ss.NewestOccurredAt, err = parseTime(newest); err != nil {
			_ = erows.Close()
			return data, fmt.Errorf("store: status: %w", err)
		}
	}
	if err := erows.Err(); err != nil {
		_ = erows.Close()
		return data, fmt.Errorf("store: status: %w", err)
	}
	_ = erows.Close()

	for _, ss := range by {
		data.Sources = append(data.Sources, *ss)
	}
	sort.Slice(data.Sources, func(i, j int) bool { return data.Sources[i].Source < data.Sources[j].Source })

	if fi, err := os.Stat(s.path); err == nil {
		data.StoreSizeBytes = fi.Size()
		// In WAL mode recent writes may sit in the -wal file; count it too.
		if wi, werr := os.Stat(s.path + "-wal"); werr == nil {
			data.StoreSizeBytes += wi.Size()
		}
	}

	var (
		r                          ReportRow
		since, before, generatedAt string
	)
	err = s.sql.QueryRowContext(ctx, `
		SELECT since, before, kind, narrowing, generated_at, generator, content
		FROM report ORDER BY generated_at DESC, id DESC LIMIT 1`).
		Scan(&since, &before, &r.Kind, &r.NarrowingJSON, &generatedAt, &r.Generator, &r.Content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return data, fmt.Errorf("store: status: %w", err)
	default:
		if r.Since, err = parseTime(since); err != nil {
			return data, fmt.Errorf("store: status: %w", err)
		}
		if r.Before, err = parseTime(before); err != nil {
			return data, fmt.Errorf("store: status: %w", err)
		}
		if r.GeneratedAt, err = parseTime(generatedAt); err != nil {
			return data, fmt.Errorf("store: status: %w", err)
		}
		data.LastReport = &r
	}
	return data, nil
}
