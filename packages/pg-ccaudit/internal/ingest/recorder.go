package ingest

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// The two feed subscriptions of the recorder-backed ingest. Each keeps its own
// cursor, persisted in the feed_cursors table.
//
//	SubAllTools   every tool_use and every tool_result with its id, is_error and
//	              body length, no body: the denominators of every rate.
//	SubErrorBodies  only the tool_results where is_error is true; their bodies
//	              (and the line around them) are read from the recorder's copy.
//
// The recorder does the coarse filtering; everything else (signature, lead_cmd,
// the line parse) stays here.
const (
	SubAllTools    = "all-tools"
	SubErrorBodies = "error-bodies"
)

// ErrRecorderDown means the recorder daemon is not answering. The ingest does
// not fall back to Claude's files: the caller reports it and polls again later.
var ErrRecorderDown = client.ErrDown

// RecorderOptions configures one recorder-backed sweep.
type RecorderOptions struct {
	// Client reads the recorder.
	Client *client.Client
	// Thinking enables thinking capture (DEFAULT OFF).
	Thinking bool
	// PageLimit is the feed page size (the daemon default when zero).
	PageLimit int
	// Now is injectable for tests.
	Now func() time.Time
	// Progress receives the final summary line. nil discards it.
	Progress io.Writer
}

// RecorderStats is the outcome of a recorder-backed sweep.
type RecorderStats struct {
	Entries    int
	Resets     int
	Resyncs    int
	LinesRead  int
	ToolCalls  int64
	ToolResult int64
	Errors     int64
	Elapsed    time.Duration
}

// Summary is one flat key=value line.
func (s RecorderStats) Summary() string {
	return fmt.Sprintf("ingest complete: entries=%d resets=%d resyncs=%d lines_read=%d tool_calls=%d tool_results=%d errors=%d elapsed=%s",
		s.Entries, s.Resets, s.Resyncs, s.LinesRead, s.ToolCalls, s.ToolResult, s.Errors, s.Elapsed.Round(time.Millisecond))
}

type subscription struct {
	name    string
	filter  *client.Filter
	project []string
}

func rawJSON(v string) json.RawMessage { return json.RawMessage(v) }

func subscriptions() []subscription {
	toolTypes := &client.Filter{Field: "type", In: []any{"tool_use", "tool_result"}}
	errResults := &client.Filter{All: []client.Filter{
		{Field: "type", Eq: rawJSON(`"tool_result"`)},
		{Field: "is_error", Eq: rawJSON(`true`)},
	}}
	return []subscription{
		{SubAllTools, toolTypes, []string{"type", "session", "time", "tool_use_id", "is_error", "body_length"}},
		{SubErrorBodies, errResults, []string{"type", "session", "time", "tool_use_id", "is_error", "body_length"}},
	}
}

// RunRecorder performs one batch sweep: for each subscription it catches up to
// the feed head and returns. A subscriber with no stored cursor starts from a
// snapshot; an expired cursor resyncs from a snapshot.
func RunRecorder(ctx context.Context, db *sql.DB, opt RecorderOptions) (RecorderStats, error) {
	start := time.Now()
	var st RecorderStats
	if opt.Client == nil {
		return st, ErrRecorderDown
	}
	for _, s := range subscriptions() {
		if err := runSub(ctx, db, opt, s, &st); err != nil {
			return st, err
		}
	}
	st.Elapsed = time.Since(start)
	if opt.Progress != nil {
		fmt.Fprintln(opt.Progress, st.Summary())
	}
	return st, nil
}

func loadCursor(ctx context.Context, db *sql.DB, name string) (string, error) {
	var c string
	err := db.QueryRowContext(ctx, `SELECT cursor FROM feed_cursors WHERE name = ?`, name).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return c, err
}

func saveCursor(ctx context.Context, tx *sql.Tx, name, cur string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO feed_cursors (name, cursor) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET cursor = excluded.cursor`, name, cur)
	return err
}

func runSub(ctx context.Context, db *sql.DB, opt RecorderOptions, s subscription, st *RecorderStats) error {
	cur, err := loadCursor(ctx, db, s.name)
	if err != nil {
		return fmt.Errorf("read cursor %s: %w", s.name, err)
	}
	if cur == "" {
		if cur, err = resync(ctx, db, opt, s, st); err != nil {
			return err
		}
	}
	for {
		ans, err := opt.Client.FeedPage(ctx, s.filter, cur, s.project, opt.PageLimit, 0)
		if err != nil {
			if errors.Is(err, client.ErrCursorExpired) {
				if cur, err = resync(ctx, db, opt, s, st); err != nil {
					return err
				}
				st.Resyncs++
				continue
			}
			return fmt.Errorf("feed %s: %w", s.name, err)
		}
		if err := applyEntries(ctx, db, opt, s.name, ans.Entries, ans.NextCursor, st); err != nil {
			return err
		}
		cur = ans.NextCursor
		if !ans.More {
			return nil
		}
	}
}

// resync reads a snapshot of the subscription's filter and stores the cursor to
// tail from. Records already held are upserted again, so it is safe to repeat.
func resync(ctx context.Context, db *sql.DB, opt RecorderOptions, s subscription, st *RecorderStats) (string, error) {
	req := client.SnapshotRequest{Where: s.filter, Project: s.project, Limit: opt.PageLimit}
	first, err := opt.Client.Snapshot(ctx, req)
	if err != nil {
		return "", fmt.Errorf("snapshot %s: %w", s.name, err)
	}
	at := first.Cursor
	page := first
	for {
		last := ""
		if !page.More {
			last = at
		}
		if err := applyEntries(ctx, db, opt, s.name, page.Entries, last, st); err != nil {
			return "", err
		}
		if !page.More {
			return at, nil
		}
		req.At, req.Page = at, page.NextPage
		if page, err = opt.Client.Snapshot(ctx, req); err != nil {
			return "", fmt.Errorf("snapshot %s: %w", s.name, err)
		}
	}
}

// pathOf names a recorder file generation; it is the path column of every row.
func pathOf(file string, gen int) string { return fmt.Sprintf("%s/g%d", file, gen) }

type lineKey struct {
	file   string
	gen    int
	offset int64
}

func fieldStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func fieldBool(m map[string]any, k string) bool  { b, _ := m[k].(bool); return b }
func fieldInt(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

// readLine fetches the whole transcript line a record sits in.
func readLine(ctx context.Context, c *client.Client, en client.Entry) (string, error) {
	ref := *en.Record
	ans, err := c.Raw(ctx, client.RawRequest{Record: &ref})
	if err != nil {
		return "", err
	}
	if ans.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(ans.Data)
		return string(b), err
	}
	return ans.Data, nil
}

// applyEntries writes one page in one transaction together with the new cursor
// (cur, when non-empty), so a crash never leaves the cursor ahead of the data.
// A reset is applied before the records after it, and a record already held is
// an upsert on its natural key, so at-least-once delivery cannot double-count.
func applyEntries(ctx context.Context, db *sql.DB, opt RecorderOptions, sub string, entries []client.Entry, cur string, st *RecorderStats) error {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	lines := map[lineKey]string{}
	for _, en := range entries {
		if en.Kind != "record" || en.Record == nil {
			continue
		}
		wantLine := fieldStr(en.Fields, "type") == "tool_use" || fieldBool(en.Fields, "is_error")
		k := lineKey{en.Record.File, en.Record.Gen, en.Record.Offset}
		if _, ok := lines[k]; wantLine && !ok {
			raw, err := readLine(ctx, opt.Client, en)
			if err != nil {
				return fmt.Errorf("read line %s@%d: %w", pathOf(k.file, k.gen), k.offset, err)
			}
			lines[k] = raw
			st.LinesRead++
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ensured := map[string]bool{}
	ensure := func(path string) error {
		if ensured[path] {
			return nil
		}
		ensured[path] = true
		_, err := tx.ExecContext(ctx, `INSERT INTO files (path, project_dir, size, mtime, resume_offset, complete, lines_ok, lines_bad, ingested_at)
			VALUES (?, '', 0, 0, 0, 0, 0, 0, ?) ON CONFLICT(path) DO UPDATE SET ingested_at = excluded.ingested_at`, path, now().Unix())
		return err
	}
	for _, en := range entries {
		st.Entries++
		switch en.Kind {
		case "reset":
			// Only the all-tools subscription applies a reset: it covers every file the
			// error-bodies one does, and the two write the same tables, so a second
			// delete would wipe the rows the first just rewrote.
			if sub != SubAllTools {
				continue
			}
			st.Resets++
			p := pathOf(en.File, en.Gen)
			for _, stmt := range []string{
				`DELETE FROM user_text WHERE path = ?`, `DELETE FROM assistant_text WHERE path = ?`,
				`DELETE FROM tool_results WHERE path = ?`, `DELETE FROM tool_calls WHERE path = ?`,
				`DELETE FROM events WHERE path = ?`,
			} {
				if _, err := tx.ExecContext(ctx, stmt, p); err != nil {
					return fmt.Errorf("apply reset %s: %w", p, err)
				}
			}
			delete(ensured, p)
			// The lines read for this page already come from the re-parsed copy, so
			// they are kept: the records after the reset need them.
		case "record":
			if en.Record == nil {
				continue
			}
			p := pathOf(en.Record.File, en.Record.Gen)
			if err := ensure(p); err != nil {
				return err
			}
			if err := applyRecord(ctx, tx, opt, sub, p, en, lines, st); err != nil {
				return err
			}
		}
	}
	if cur != "" {
		if err := saveCursor(ctx, tx, sub, cur); err != nil {
			return fmt.Errorf("save cursor %s: %w", sub, err)
		}
	}
	return tx.Commit()
}

func applyRecord(ctx context.Context, tx *sql.Tx, opt RecorderOptions, sub, path string, en client.Entry, lines map[lineKey]string, st *RecorderStats) error {
	typ := fieldStr(en.Fields, "type")
	id := fieldStr(en.Fields, "tool_use_id")
	seq := en.Record.Offset
	k := lineKey{en.Record.File, en.Record.Gen, seq}
	if raw, ok := lines[k]; ok {
		// The whole line is parsed with the same code the file reader used, so
		// events, calls, results and narration keep their shape; the length of
		// every body comes from the recorder's own fact.
		var p parsedFile
		p.appendLine(seq, []byte(raw), opt.Thinking)
		if err := writeParsed(ctx, tx, path, p, opt.Thinking); err != nil {
			return err
		}
		tally(sub, typ, en, st)
		return nil
	}
	if typ != "tool_result" || id == "" {
		return nil
	}
	// A success result: length only, no body. The event row is minimal and never
	// replaces a full one already written from the line.
	sess := fieldStr(en.Fields, "session")
	ts := fieldStr(en.Fields, "time")
	if _, err := tx.ExecContext(ctx, `INSERT INTO events (path, seq, session_id, ts, type) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), 'user')
		ON CONFLICT(path, seq) DO NOTHING`, path, seq, sess, ts); err != nil {
		return fmt.Errorf("insert event %s#%d: %w", path, seq, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(tool_use_id) DO NOTHING`,
		id, path, seq, boolInt(fieldBool(en.Fields, "is_error")), fieldInt(en.Fields, "body_length")); err != nil {
		return fmt.Errorf("insert tool_result %s: %w", id, err)
	}
	tally(sub, typ, en, st)
	return nil
}

// tally attributes an entry once: calls and results by the all-tools
// subscription, errors by the error-bodies one.
func tally(sub, typ string, en client.Entry, st *RecorderStats) {
	switch {
	case sub == SubAllTools && typ == "tool_use":
		st.ToolCalls++
	case sub == SubAllTools && typ == "tool_result":
		st.ToolResult++
	case sub == SubErrorBodies && typ == "tool_result" && fieldBool(en.Fields, "is_error"):
		st.Errors++
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// IsRecorderPath reports whether a files.path was written by this ingest.
func IsRecorderPath(p string) bool { return strings.Contains(p, "/g") && strings.HasPrefix(p, "f_") }
