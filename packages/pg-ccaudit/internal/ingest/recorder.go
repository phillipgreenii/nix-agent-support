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

// The four feed subscriptions of the recorder-backed ingest. Every one names
// source claude-transcript (so a status file is never ingested) and keeps its own
// cursor, persisted in the feed_cursors table.
//
//	SubLines   every transcript line record with its indexed scalar fields and no
//	           bodies: the events rows. A system line also gets one raw read, for
//	           hookCount and hookErrors, which the recorder does not index.
//	SubTools   every tool_use and tool_result with its id, is_error and body
//	           length, no body (the denominators of every rate). A tool_use's input
//	           is an opaque field the feed cannot project, so its line is read once.
//	SubErrors  the tool_results where is_error is true; their bodies are read from
//	           the recorder's copy (a body is not a projectable field).
//	SubText    the human and assistant text records (and thinking blocks only while
//	           the thinking capture flag is on); the text is not projectable either,
//	           so the line is read once and parsed with the same code the file
//	           reader used.
//
// Per-file order: ccaudit's seq is the record id's offset within one file id and
// generation (path is "<file>/g<gen>"). The item index inside a line is not
// needed in the schema: every ccaudit table keeps one row per line per kind, or is
// keyed by tool_use_id.
//
// The recorder does the coarse filtering; the signature, lead_cmd and the line
// parse stay here.
const (
	SubLines  = "lines"
	SubTools  = "tools"
	SubErrors = "errors"
	SubText   = "text"
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

	// beforeSnapshot is a test seam called before every snapshot request; a
	// non-nil error stands in for that request failing (a recorder deadline).
	beforeSnapshot func(req client.SnapshotRequest) error
}

// RecorderStats is the outcome of a recorder-backed sweep.
type RecorderStats struct {
	Entries    int
	Resets     int
	Resyncs    int
	LinesRead  int
	Lines      int
	Texts      int
	ToolCalls  int64
	ToolResult int64
	Errors     int64
	Elapsed    time.Duration
}

// Summary is one flat key=value line.
func (s RecorderStats) Summary() string {
	return fmt.Sprintf("ingest complete: entries=%d resets=%d resyncs=%d lines=%d texts=%d lines_read=%d tool_calls=%d tool_results=%d errors=%d elapsed=%s",
		s.Entries, s.Resets, s.Resyncs, s.Lines, s.Texts, s.LinesRead, s.ToolCalls, s.ToolResult, s.Errors, s.Elapsed.Round(time.Millisecond))
}

type subscription struct {
	name    string
	filter  *client.Filter
	project []string
}

func rawJSON(v string) json.RawMessage { return json.RawMessage(v) }

func transcript() client.Filter {
	return client.Filter{Field: "source", Eq: rawJSON(`"claude-transcript"`)}
}

func subscriptions(thinking bool) []subscription {
	lines := &client.Filter{All: []client.Filter{transcript(), {Field: "item", Eq: rawJSON(`-1`)}}}
	tools := &client.Filter{All: []client.Filter{transcript(), {Field: "type", In: []any{"tool_use", "tool_result"}}}}
	errs := &client.Filter{All: []client.Filter{
		transcript(),
		{Field: "type", Eq: rawJSON(`"tool_result"`)},
		{Field: "is_error", Eq: rawJSON(`true`)},
	}}
	types := []any{"text"}
	if thinking {
		types = append(types, "thinking")
	}
	text := &client.Filter{All: []client.Filter{transcript(), {Field: "type", In: types}}}
	return []subscription{
		{SubLines, lines, []string{
			"type", "session", "time", "uuid", "parent_uuid", "is_sidechain", "cwd",
			"git_branch", "permission_mode", "duration_ms", "user_type", "source_tool_assistant_uuid",
			"prompt_id", "entrypoint",
		}},
		{SubTools, tools, []string{"type", "session", "time", "tool_use_id", "is_error", "body_length"}},
		{SubErrors, errs, []string{"type", "session", "time", "tool_use_id", "is_error", "body_length"}},
		{SubText, text, []string{"type", "session", "time"}},
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
	for _, s := range subscriptions(opt.Thinking) {
		resets := st.Resets
		if err := runSub(ctx, db, opt, s, &st); err != nil {
			return st, err
		}
		if s.name == SubLines && st.Resets > resets {
			// A reset wiped the rows of a re-parsed file for every table. The other
			// subscriptions' cursors may already be past the records that rewrite
			// them, so they start over from a snapshot (idempotent upserts).
			if _, err := db.ExecContext(ctx, `DELETE FROM feed_cursors WHERE name NOT IN (?, ?)`, SubLines, snapName(SubLines)); err != nil {
				return st, fmt.Errorf("rewind cursors after a reset: %w", err)
			}
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

// snapName is the feed_cursors row that holds a subscription's snapshot
// position while its initial snapshot is under way. The value is
// "<at cursor>|<next page token>": the pair the recorder needs to continue the
// snapshot at the next page. The row exists only between the first page and the
// last, so a subscription's own cursor row is still absent (a cursor is saved
// only with the last page) and a crash or a deadline resumes instead of
// restarting from the first page.
func snapName(sub string) string { return sub + ".snapshot" }

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
		if err := applyEntries(ctx, db, opt, s.name, ans.Entries, cursorWriter(s.name, ans.NextCursor), st); err != nil {
			return err
		}
		cur = ans.NextCursor
		if !ans.More {
			return nil
		}
	}
}

// cursorWriter saves cur as the subscription's cursor in the page's transaction.
func cursorWriter(name, cur string) func(*sql.Tx) error {
	if cur == "" {
		return nil
	}
	return func(tx *sql.Tx) error {
		return saveCursor(context.Background(), tx, name, cur)
	}
}

// resync reads a snapshot of the subscription's filter and stores the cursor to
// tail from. Records already held are upserted again, so it is safe to repeat.
// Every page is committed together with the snapshot position, so a run that
// ends part-way (a recorder deadline, a crash) continues at the next page on the
// next run. A position the recorder no longer honours restarts from the top.
func resync(ctx context.Context, db *sql.DB, opt RecorderOptions, s subscription, st *RecorderStats) (string, error) {
	cur, err := resumeSnapshot(ctx, db, opt, s, st)
	if err == nil || !errors.Is(err, client.ErrCursorExpired) {
		return cur, err
	}
	if _, derr := db.ExecContext(ctx, `DELETE FROM feed_cursors WHERE name = ?`, snapName(s.name)); derr != nil {
		return "", fmt.Errorf("drop snapshot position %s: %w", s.name, derr)
	}
	return resumeSnapshot(ctx, db, opt, s, st)
}

func snapshot(ctx context.Context, opt RecorderOptions, s subscription, req client.SnapshotRequest) (*client.SnapshotAnswer, error) {
	if opt.beforeSnapshot != nil {
		if err := opt.beforeSnapshot(req); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", s.name, err)
		}
	}
	ans, err := opt.Client.Snapshot(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", s.name, err)
	}
	return ans, nil
}

func resumeSnapshot(ctx context.Context, db *sql.DB, opt RecorderOptions, s subscription, st *RecorderStats) (string, error) {
	req := client.SnapshotRequest{Where: s.filter, Project: s.project, Limit: opt.PageLimit}
	saved, err := loadCursor(ctx, db, snapName(s.name))
	if err != nil {
		return "", fmt.Errorf("read snapshot position %s: %w", s.name, err)
	}
	var at string
	var page *client.SnapshotAnswer
	if i := strings.Index(saved, "|"); i > 0 {
		at, req.At, req.Page = saved[:i], saved[:i], saved[i+1:]
	} else {
		if page, err = snapshot(ctx, opt, s, req); err != nil {
			return "", err
		}
		at = page.Cursor
		req.At = at
	}
	for {
		if page == nil {
			if page, err = snapshot(ctx, opt, s, req); err != nil {
				return "", err
			}
		}
		pg := page
		page = nil
		save := func(tx *sql.Tx) error {
			if pg.More {
				return saveCursor(ctx, tx, snapName(s.name), at+"|"+pg.NextPage)
			}
			if err := saveCursor(ctx, tx, s.name, at); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM feed_cursors WHERE name = ?`, snapName(s.name))
			return err
		}
		if err := applyEntries(ctx, db, opt, s.name, pg.Entries, save, st); err != nil {
			return "", err
		}
		if !pg.More {
			return at, nil
		}
		req.Page = pg.NextPage
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

// wantsLine reports whether a record of this subscription needs its whole
// transcript line read (the fields the feed cannot project: a tool_use input,
// an error body, text, and a system line's hook keys).
func wantsLine(sub string, en client.Entry) bool {
	typ := fieldStr(en.Fields, "type")
	switch sub {
	case SubLines:
		return typ == "system"
	case SubTools:
		return typ == "tool_use"
	case SubErrors, SubText:
		return true
	}
	return false
}

// applyEntries writes one page in one transaction together with the new cursor
// (finish, which saves the cursor or snapshot position), so a crash never leaves the cursor ahead of the data.
// A reset is applied before the records after it, and a record already held is
// an upsert on its natural key, so at-least-once delivery cannot double-count.
func applyEntries(ctx context.Context, db *sql.DB, opt RecorderOptions, sub string, entries []client.Entry, finish func(*sql.Tx) error, st *RecorderStats) error {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	lines := map[lineKey]string{}
	for _, en := range entries {
		if en.Kind != "record" || en.Record == nil || !wantsLine(sub, en) {
			continue
		}
		k := lineKey{en.Record.File, en.Record.Gen, en.Record.Offset}
		if _, ok := lines[k]; ok {
			continue
		}
		raw, err := readLine(ctx, opt.Client, en)
		if err != nil {
			return fmt.Errorf("read line %s@%d: %w", pathOf(k.file, k.gen), k.offset, err)
		}
		lines[k] = raw
		st.LinesRead++
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
			// Only the lines subscription applies a reset: it covers every file the
			// others do, and they write the same tables, so a second delete would
			// wipe rows the first just rewrote. RunRecorder rewinds the others.
			if sub != SubLines {
				continue
			}
			st.Resets++
			p := pathOf(en.File, en.Gen)
			stmts := []string{
				`DELETE FROM user_text WHERE path = ?`, `DELETE FROM assistant_text WHERE path = ?`,
				`DELETE FROM tool_results WHERE path = ?`, `DELETE FROM tool_calls WHERE path = ?`,
				`DELETE FROM events WHERE path = ?`,
			}
			if opt.Thinking {
				stmts = append(stmts, `DELETE FROM thinking WHERE path = ?`)
			}
			for _, stmt := range stmts {
				if _, err := tx.ExecContext(ctx, stmt, p); err != nil {
					return fmt.Errorf("apply reset %s: %w", p, err)
				}
			}
			delete(ensured, p)
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
	if finish != nil {
		if err := finish(tx); err != nil {
			return fmt.Errorf("save cursor %s: %w", sub, err)
		}
	}
	return tx.Commit()
}

// isoMillis renders the recorder's time the way Claude writes it, so ordering by
// the ts text stays chronological within one second.
func isoMillis(s string) any {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func nullStr(m map[string]any, k string) any {
	if s := fieldStr(m, k); s != "" {
		return s
	}
	return nil
}

func nullBool(m map[string]any, k string) any {
	if b, ok := m[k].(bool); ok {
		return boolInt(b)
	}
	return nil
}

func nullInt(m map[string]any, k string) any {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return nil
}

func applyRecord(ctx context.Context, tx *sql.Tx, opt RecorderOptions, sub, path string, en client.Entry, lines map[lineKey]string, st *RecorderStats) error {
	typ := fieldStr(en.Fields, "type")
	seq := en.Record.Offset
	k := lineKey{en.Record.File, en.Record.Gen, seq}
	if raw, ok := lines[k]; ok {
		// The whole line is parsed with the same code the file reader used, so
		// events, calls, results, narration and user text keep their shape.
		var p parsedFile
		p.appendLine(seq, []byte(raw), opt.Thinking)
		if err := writeParsed(ctx, tx, path, p, opt.Thinking); err != nil {
			return err
		}
		tally(sub, typ, en, st)
		return nil
	}
	switch {
	case sub == SubLines:
		// Indexed scalar fields only. The raw-derived columns (prompt_source,
		// hook_count, hook_errors) are left as they are on a conflict.
		f := en.Fields
		_, err := tx.ExecContext(ctx, `INSERT INTO events (path, seq, uuid, parent_uuid, session_id, ts, type, is_sidechain,
				cwd, git_branch, permission_mode, duration_ms, user_type, source_tool_assistant_uuid, prompt_id, entrypoint)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(path, seq) DO UPDATE SET
				uuid=excluded.uuid, parent_uuid=excluded.parent_uuid, session_id=excluded.session_id,
				ts=excluded.ts, type=excluded.type, is_sidechain=excluded.is_sidechain, cwd=excluded.cwd,
				git_branch=excluded.git_branch, permission_mode=excluded.permission_mode,
				duration_ms=excluded.duration_ms, user_type=excluded.user_type,
				source_tool_assistant_uuid=excluded.source_tool_assistant_uuid,
				prompt_id=excluded.prompt_id, entrypoint=excluded.entrypoint`,
			path, seq, nullStr(f, "uuid"), nullStr(f, "parent_uuid"), nullStr(f, "session"), isoMillis(fieldStr(f, "time")),
			typ, nullBool(f, "is_sidechain"), nullStr(f, "cwd"), nullStr(f, "git_branch"), nullStr(f, "permission_mode"),
			nullInt(f, "duration_ms"), nullStr(f, "user_type"), nullStr(f, "source_tool_assistant_uuid"),
			nullStr(f, "prompt_id"), nullStr(f, "entrypoint"))
		if err != nil {
			return fmt.Errorf("insert event %s#%d: %w", path, seq, err)
		}
		tally(sub, typ, en, st)
	case sub == SubTools && typ == "tool_result" && fieldStr(en.Fields, "tool_use_id") != "":
		// A tool result without its body: length only. It never replaces a row
		// that already carries an error body.
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT(tool_use_id) DO UPDATE SET
				path=excluded.path, seq=excluded.seq, content_len=excluded.content_len`,
			fieldStr(en.Fields, "tool_use_id"), path, seq, boolInt(fieldBool(en.Fields, "is_error")), fieldInt(en.Fields, "body_length")); err != nil {
			return fmt.Errorf("insert tool_result %s: %w", fieldStr(en.Fields, "tool_use_id"), err)
		}
		tally(sub, typ, en, st)
	}
	return nil
}

// tally attributes an entry once, by the subscription that owns the count.
func tally(sub, typ string, en client.Entry, st *RecorderStats) {
	switch {
	case sub == SubLines:
		st.Lines++
	case sub == SubTools && typ == "tool_use":
		st.ToolCalls++
	case sub == SubTools && typ == "tool_result":
		st.ToolResult++
	case sub == SubErrors && typ == "tool_result" && fieldBool(en.Fields, "is_error"):
		st.Errors++
	case sub == SubText:
		st.Texts++
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
