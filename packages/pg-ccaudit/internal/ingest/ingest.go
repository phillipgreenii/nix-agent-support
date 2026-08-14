// Package ingest indexes Claude Code transcripts into the SQLite store.
//
// It is a single Go program rather than a shell pipeline on purpose (T-7): the
// BSD/GNU divergence in sed/awk/xargs, plus BSD regex handling on non-UTF-8
// bytes, cost two stalled agent runs before this existed, and that must never
// again be a precondition for asking a question about the corpus.
//
// Two properties are prioritised over throughput, deliberately:
//
//  1. RESUMABILITY BY BYTE OFFSET (T-1a). Transcripts are append-only while a
//     session runs, so re-parsing a whole growing file every tick would mean
//     re-parsing the ACTIVE session continuously. Offset-resume is what makes a
//     ~15 minute sweep affordable at all.
//
//  2. NEVER DOUBLE-COUNTING A RECORD. Every write is an upsert on a natural
//     key — (path, seq) for events/assistant_text, tool_use_id for
//     calls/results — so re-ingesting a byte range is a no-op rather than a
//     duplicate. Combined with a per-file transaction that commits the records
//     and the new resume offset TOGETHER, no crash can leave the offset ahead of
//     the data or the data ahead of the offset.
//
// Where speed and correctness conflict, correctness wins; the resume path even
// spends one extra byte read per file to prove the stored offset still lands on
// a record boundary.
package ingest

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultProgressEvery is how many scanned files pass between progress lines
// (T-8). A supervising agent's watchdog needs to observe liveness; the audit
// that motivated this tool stalled a 600 s watchdog twice because its extraction
// was silent.
const DefaultProgressEvery = 25

// DefaultFinalAfter is how long a file must be quiescent before its fully
// consumed state is recorded as `complete = 1` (T-15). One sweep interval: if
// the file has not changed in longer than the tick that would have caught a
// change, treating it as final is safe, and if it grows anyway the next tick
// resumes it from its offset regardless.
const DefaultFinalAfter = 15 * time.Minute

// FinalAfterImmediate marks a fully consumed file complete with NO quiescence
// window. It is a distinct sentinel rather than a plain zero because zero is
// also the "caller said nothing" value, and silently reading an unset field as
// "final immediately" would mark a live session's transcript final — precisely
// the confusion T-15 exists to prevent.
const FinalAfterImmediate = time.Duration(-1)

// Options configures a sweep.
type Options struct {
	// Root is the transcript root (…/.claude/projects).
	Root string
	// Thinking enables T-16 thinking capture. DEFAULT OFF.
	Thinking bool
	// ProgressEvery is the progress cadence in files; 0 uses the default.
	ProgressEvery int
	// FinalAfter is the quiescence window for `complete`; 0 uses the default.
	FinalAfter time.Duration
	// Now is injectable for tests.
	Now func() time.Time
	// Progress receives T-8 progress lines. nil discards them.
	Progress io.Writer
}

// Stats is the outcome of a sweep. The zero-work case — Changed 0, BytesParsed
// 0 — is the observable proof that ingest is incremental (T-1).
type Stats struct {
	Scanned     int
	Unchanged   int
	Resumed     int
	Fresh       int
	Reingested  int
	Failed      int
	BytesParsed int64
	LinesOK     int64
	LinesBad    int64
	Events      int64
	ToolCalls   int64
	ToolResults int64
	Errors      int64
	Narration   int64
	// UserProse counts user records carrying prose (a turn, not a tool result).
	UserProse int64
	// HumanTurns counts the subset whose text was stored in full — the typed and
	// queued turns. The gap between the two is the T-3a economy made observable.
	HumanTurns    int64
	Interruptions int64
	Thinking      int64
	Elapsed       time.Duration
}

// Changed is the number of files this sweep actually parsed bytes from.
func (s Stats) Changed() int { return s.Fresh + s.Resumed + s.Reingested }

// Summary is the final T-8 line. It is deliberately one flat key=value line so a
// supervising agent can assert on it without a parser.
func (s Stats) Summary() string {
	return fmt.Sprintf(
		"ingest complete: scanned=%d changed=%d unchanged=%d fresh=%d resumed=%d reingested=%d failed=%d "+
			"bytes=%d lines_ok=%d lines_bad=%d events=%d tool_calls=%d tool_results=%d errors=%d narration=%d "+
			"user_prose=%d human_turns=%d interruptions=%d thinking=%d elapsed=%s",
		s.Scanned, s.Changed(), s.Unchanged, s.Fresh, s.Resumed, s.Reingested, s.Failed,
		s.BytesParsed, s.LinesOK, s.LinesBad, s.Events, s.ToolCalls, s.ToolResults, s.Errors,
		s.Narration, s.UserProse, s.HumanTurns, s.Interruptions, s.Thinking,
		s.Elapsed.Round(time.Millisecond),
	)
}

type fileState struct {
	size         int64
	mtime        int64
	resumeOffset int64
	linesOK      int64
	linesBad     int64
}

type mode int

const (
	modeUnchanged mode = iota
	modeFresh
	modeResume
	modeReingest
)

func (m mode) String() string {
	switch m {
	case modeFresh:
		return "fresh"
	case modeResume:
		return "resume"
	case modeReingest:
		return "reingest"
	default:
		return "unchanged"
	}
}

// Run performs one sweep.
func Run(ctx context.Context, db *sql.DB, opt Options) (Stats, error) {
	start := time.Now()
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	progressEvery := opt.ProgressEvery
	if progressEvery <= 0 {
		progressEvery = DefaultProgressEvery
	}
	finalAfter := opt.FinalAfter
	switch {
	case finalAfter == 0:
		finalAfter = DefaultFinalAfter
	case finalAfter < 0:
		finalAfter = 0
	}

	var stats Stats
	paths, err := discover(opt.Root)
	if err != nil {
		return stats, err
	}

	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := ingestFile(ctx, db, path, opt, now(), finalAfter, &stats); err != nil {
			// A single unreadable or unparseable FILE must not abort the sweep;
			// T-2's provable coverage depends on the run finishing and recording
			// what it saw. Report it and carry on.
			stats.Failed++
			if opt.Progress != nil {
				fmt.Fprintf(opt.Progress, "warn: %s: %v\n", path, err)
			}
		}
		stats.Scanned++
		if opt.Progress != nil && (i+1)%progressEvery == 0 {
			fmt.Fprintf(opt.Progress,
				"progress: scanned=%d/%d changed=%d bytes=%d lines_ok=%d lines_bad=%d errors=%d elapsed=%s\n",
				stats.Scanned, len(paths), stats.Changed(), stats.BytesParsed,
				stats.LinesOK, stats.LinesBad, stats.Errors,
				time.Since(start).Round(time.Millisecond))
		}
	}

	stats.Elapsed = time.Since(start)
	if opt.Progress != nil {
		fmt.Fprintln(opt.Progress, stats.Summary())
	}
	return stats, nil
}

// discover lists every transcript under root, sorted so a sweep is
// order-deterministic and its progress output is diffable between runs.
func discover(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is skipped, not fatal.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk transcript root %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

func ingestFile(
	ctx context.Context,
	db *sql.DB,
	path string,
	opt Options,
	now time.Time,
	finalAfter time.Duration,
	stats *Stats,
) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	// mtime is stored in NANOSECONDS. Seconds would be a correctness bug, not a
	// precision nicety: a session appending twice within the same wall-clock
	// second would present an unchanged (size, mtime) pair on a same-size
	// rewrite and the delta would be missed.
	size := st.Size()
	mtime := st.ModTime().UnixNano()

	prev, found, err := loadFileState(ctx, db, path)
	if err != nil {
		return err
	}

	m, offset, seq := decide(prev, found, size, mtime)

	// A stored offset is only trustworthy if it still lands just after a newline.
	// Verifying costs one byte read and turns a silently corrupt resume (parsing
	// from the middle of a record) into a clean re-ingest.
	if m == modeResume && offset > 0 {
		ok, err := offsetOnRecordBoundary(path, offset)
		if err != nil {
			return err
		}
		if !ok {
			m, offset, seq = modeReingest, 0, 0
		}
	}

	if m == modeUnchanged {
		stats.Unchanged++
		return nil
	}

	parsed, err := parseFrom(path, offset, seq, opt.Thinking)
	if err != nil {
		return err
	}

	newOffset := offset + parsed.consumed
	// `complete` answers "is our ingest of this file final?" (T-15). Both halves
	// are required: every visible byte consumed (so a torn trailing write is
	// never mistaken for a finished file), AND the file quiescent for at least a
	// sweep interval (so a live session's transcript is recorded as OPEN and a
	// later tick resumes it). Partial ingestion is therefore always
	// distinguishable from complete ingestion, which is what T-2's provable
	// coverage rests on.
	complete := newOffset == size && now.Sub(time.Unix(0, mtime)) >= finalAfter

	linesOK := parsed.linesOK
	linesBad := parsed.linesBad
	if m == modeResume {
		linesOK += prev.linesOK
		linesBad += prev.linesBad
	}

	if err := commitFile(ctx, db, path, fileRow{
		projectDir:   filepath.Base(filepath.Dir(path)),
		size:         size,
		mtime:        mtime,
		resumeOffset: newOffset,
		complete:     complete,
		linesOK:      linesOK,
		linesBad:     linesBad,
		ingestedAt:   now.Unix(),
	}, parsed, m == modeReingest, opt.Thinking); err != nil {
		return err
	}

	switch m {
	case modeFresh:
		stats.Fresh++
	case modeResume:
		stats.Resumed++
	case modeReingest:
		stats.Reingested++
	case modeUnchanged:
	}
	stats.BytesParsed += parsed.consumed
	stats.LinesOK += parsed.linesOK
	stats.LinesBad += parsed.linesBad
	stats.Events += int64(len(parsed.events))
	stats.ToolCalls += int64(len(parsed.calls))
	stats.ToolResults += int64(len(parsed.results))
	stats.Narration += int64(len(parsed.narration))
	stats.UserProse += int64(len(parsed.userText))
	stats.Thinking += int64(len(parsed.thinking))
	for _, u := range parsed.userText {
		if u.interrupted {
			stats.Interruptions++
		}
		if u.text != nil && !u.interrupted {
			stats.HumanTurns++
		}
	}
	for _, r := range parsed.results {
		if r.isError {
			stats.Errors++
		}
	}
	return nil
}

// decide classifies a file against its recorded state.
//
// Change DETECTION is keyed on (path, size, mtime) per T-1; the resume OFFSET
// then makes the re-parse proportional to the delta per T-1a. A file whose size
// SHRANK or whose mtime moved BACKWARD was rewritten — compaction does exactly
// that — so it is re-ingested from zero rather than resumed into the middle of
// different content.
func decide(prev fileState, found bool, size, mtime int64) (mode, int64, int64) {
	if !found {
		return modeFresh, 0, 0
	}
	if size < prev.size || mtime < prev.mtime {
		return modeReingest, 0, 0
	}
	if size == prev.size && mtime == prev.mtime {
		return modeUnchanged, 0, 0
	}
	if prev.resumeOffset > size {
		return modeReingest, 0, 0
	}
	// seq is the LINE ORDINAL within the file, and every consumed line increments
	// exactly one of lines_ok / lines_bad — so their sum IS the next ordinal. That
	// keeps the ordinal derivable from the specified DDL without a column for it.
	return modeResume, prev.resumeOffset, prev.linesOK + prev.linesBad
}

func offsetOnRecordBoundary(path string, offset int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open for boundary check: %w", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, offset-1); err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, fmt.Errorf("boundary check read: %w", err)
	}
	return buf[0] == '\n', nil
}

type eventRow struct {
	seq                     int64
	uuid                    *string
	parentUUID              *string
	sessionID               *string
	ts                      *string
	typ                     string
	isSidechain             *int64
	cwd                     *string
	gitBranch               *string
	permissionMode          *string
	durationMs              *int64
	hookCount               *int64
	hookErrors              *string
	promptSource            *string
	userType                *string
	sourceToolAssistantUUID *string
	promptID                *string
	entrypoint              *string
}

type callRow struct {
	toolUseID string
	seq       int64
	toolName  string
	inputJSON string
	leadCmd   *string
}

type resultRow struct {
	toolUseID  string
	seq        int64
	isError    bool
	contentLen int64
	content    *string
	signature  *string
}

type textRow struct {
	seq  int64
	text string
}

// userRow is one user PROSE record. text is nil for everything that is not a
// human turn, mirroring tool_results' content/content_len split (T-3a): the
// length is always recorded so the record stays countable, the body only when a
// census reads it.
type userRow struct {
	seq         int64
	textLen     int64
	text        *string
	interrupted bool
}

type parsedFile struct {
	consumed  int64
	linesOK   int64
	linesBad  int64
	events    []eventRow
	calls     []callRow
	results   []resultRow
	narration []textRow
	userText  []userRow
	thinking  []textRow
}

// parseFrom reads from offset and returns the records in the delta.
//
// Tolerance is PER LINE, never per file (T-2): a line that does not decode is
// counted in lines_bad and skipped, so one corrupt record costs one record
// rather than a whole session's coverage. A TRAILING line with no newline is a
// torn write from a session that is still appending — it is neither parsed nor
// counted, and `consumed` stops before it, so the next sweep re-reads it whole.
func parseFrom(path string, offset, startSeq int64, withThinking bool) (parsedFile, error) {
	var out parsedFile
	f, err := os.Open(path)
	if err != nil {
		return out, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return out, fmt.Errorf("seek to %d: %w", offset, err)
		}
	}
	r := bufio.NewReaderSize(f, 1<<20)
	seq := startSeq
	for {
		raw, err := r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Whatever was read here has no terminator: an incomplete
				// record. Leave it unconsumed.
				break
			}
			return out, fmt.Errorf("read: %w", err)
		}
		out.consumed += int64(len(raw))
		out.appendLine(seq, raw, withThinking)
		seq++
	}
	return out, nil
}

func (p *parsedFile) appendLine(seq int64, raw []byte, withThinking bool) {
	trimmed := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(trimmed) == "" {
		// A blank line is not a record. It counts as bad rather than being
		// ignored, because lines_ok + lines_bad must stay equal to the number of
		// lines consumed — that identity is what makes the line ordinal
		// derivable on resume.
		p.linesBad++
		return
	}
	var l line
	if err := json.Unmarshal([]byte(trimmed), &l); err != nil {
		p.linesBad++
		return
	}
	p.linesOK++

	p.events = append(p.events, eventRow{
		seq:                     seq,
		uuid:                    l.UUID,
		parentUUID:              l.ParentUUID,
		sessionID:               l.SessionID,
		ts:                      l.Timestamp,
		typ:                     l.Type,
		isSidechain:             boolToInt(l.IsSidechain),
		cwd:                     l.Cwd,
		gitBranch:               l.GitBranch,
		permissionMode:          l.PermissionMode,
		durationMs:              l.DurationMs,
		hookCount:               l.HookCount,
		hookErrors:              hookErrorsText(l.HookErrors),
		promptSource:            l.PromptSource,
		userType:                l.UserType,
		sourceToolAssistantUUID: l.SourceToolAssistantUUID,
		promptID:                l.PromptID,
		entrypoint:              l.Entrypoint,
	})

	if l.Message == nil {
		return
	}
	bs := blocks(l.Message.Content)

	// The user branch runs BEFORE the len(bs) == 0 guard on purpose: a typed human
	// turn's content is a plain STRING, so it decodes to no blocks at all, and
	// returning early on an empty block list would discard every human turn in the
	// corpus — the exact records the mistake census is built on.
	if l.Type == "user" {
		p.appendUserProse(seq, l, bs)
	}

	if len(bs) == 0 {
		return
	}

	for _, b := range bs {
		switch b.Type {
		case "tool_use":
			if b.ID == "" {
				continue
			}
			input := "{}"
			if len(b.Input) > 0 {
				// Stored UNTRUNCATED (T-3). Truncating inputs at extraction time
				// is what manufactured a 470-row phantom lead-command bucket in
				// the census this index replaces.
				input = string(b.Input)
			}
			var lead *string
			if b.Name == "Bash" {
				v := LeadCmd(b.Input)
				lead = &v
			}
			p.calls = append(p.calls, callRow{
				toolUseID: b.ID,
				seq:       seq,
				toolName:  b.Name,
				inputJSON: input,
				leadCmd:   lead,
			})
		case "tool_result":
			if b.ToolUseID == "" {
				continue
			}
			body := flattenContent(b.Content)
			row := resultRow{
				toolUseID:  b.ToolUseID,
				seq:        seq,
				isError:    b.isError(),
				contentLen: contentLen(body),
			}
			if row.isError {
				// Error bodies in full (T-3), plus the normalized signature
				// (T-6). A SUCCESSFUL body is never stored — length only (T-3a).
				c := body
				sig := Signature(body)
				row.content = &c
				row.signature = &sig
			}
			p.results = append(p.results, row)
		}
	}

	if l.Type == "assistant" {
		if txt := assistantText(bs); txt != "" {
			p.narration = append(p.narration, textRow{seq: seq, text: txt})
		}
		if withThinking {
			if txt := thinkingText(bs); txt != "" {
				p.thinking = append(p.thinking, textRow{seq: seq, text: txt})
			}
		}
	}
}

// appendUserProse records one user PROSE record, applying T-3a's rule to the
// human side of the conversation.
func (p *parsedFile) appendUserProse(seq int64, l line, bs []block) {
	if carriesToolResult(bs) {
		return
	}
	prose := userProse(l.Message.Content)
	if prose == "" {
		return
	}
	row := userRow{
		seq:         seq,
		textLen:     contentLen(prose),
		interrupted: strings.HasPrefix(strings.TrimSpace(prose), InterruptionPrefix),
	}
	// Stored in full for a human turn, and for the interruption sentinel (a fixed
	// ~35-rune string, so keeping it costs nothing and lets a candidate carry its
	// own excerpt instead of forcing a reader back to the transcript).
	if row.interrupted || (l.PromptSource != nil && HumanPromptSources[*l.PromptSource]) {
		t := prose
		row.text = &t
	}
	p.userText = append(p.userText, row)
}

func loadFileState(ctx context.Context, db *sql.DB, path string) (fileState, bool, error) {
	var fsx fileState
	err := db.QueryRowContext(
		ctx,
		`SELECT size, mtime, resume_offset, lines_ok, lines_bad FROM files WHERE path = ?`, path,
	).Scan(&fsx.size, &fsx.mtime, &fsx.resumeOffset, &fsx.linesOK, &fsx.linesBad)
	if errors.Is(err, sql.ErrNoRows) {
		return fsx, false, nil
	}
	if err != nil {
		return fsx, false, fmt.Errorf("read files row for %s: %w", path, err)
	}
	return fsx, true, nil
}

type fileRow struct {
	projectDir   string
	size         int64
	mtime        int64
	resumeOffset int64
	complete     bool
	linesOK      int64
	linesBad     int64
	ingestedAt   int64
}

// commitFile writes the delta AND the new resume offset in ONE transaction.
// Atomicity here is the whole guarantee: split across two commits, a crash
// between them would either lose records (offset ahead of data) or, without the
// upserts below, duplicate them.
func commitFile(
	ctx context.Context,
	db *sql.DB,
	path string,
	fr fileRow,
	parsed parsedFile,
	purge bool,
	withThinking bool,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if purge {
		// A rewritten file's old rows are deleted explicitly rather than relying
		// on the ON DELETE CASCADE, which the specified DDL declares on events
		// alone — tool_calls, tool_results and assistant_text carry a bare path
		// column with no foreign key.
		for _, stmt := range []string{
			`DELETE FROM user_text WHERE path = ?`,
			`DELETE FROM assistant_text WHERE path = ?`,
			`DELETE FROM tool_results WHERE path = ?`,
			`DELETE FROM tool_calls WHERE path = ?`,
			`DELETE FROM events WHERE path = ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, path); err != nil {
				return fmt.Errorf("purge %s: %w", path, err)
			}
		}
		if withThinking {
			if _, err := tx.ExecContext(ctx, `DELETE FROM thinking WHERE path = ?`, path); err != nil {
				return fmt.Errorf("purge thinking for %s: %w", path, err)
			}
		}
	}

	completeVal := 0
	if fr.complete {
		completeVal = 1
	}
	// The files row must land BEFORE the events rows: events.path is a foreign
	// key into it.
	if _, err := tx.ExecContext(
		ctx, `
		INSERT INTO files (path, project_dir, size, mtime, resume_offset, complete, lines_ok, lines_bad, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			project_dir = excluded.project_dir,
			size = excluded.size,
			mtime = excluded.mtime,
			resume_offset = excluded.resume_offset,
			complete = excluded.complete,
			lines_ok = excluded.lines_ok,
			lines_bad = excluded.lines_bad,
			ingested_at = excluded.ingested_at`,
		path, fr.projectDir, fr.size, fr.mtime, fr.resumeOffset, completeVal,
		fr.linesOK, fr.linesBad, fr.ingestedAt,
	); err != nil {
		return fmt.Errorf("upsert files row for %s: %w", path, err)
	}

	// Every insert below is an upsert on the row's natural key, so replaying a
	// byte range can never double-count.
	evStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (path, seq, uuid, parent_uuid, session_id, ts, type, is_sidechain,
			cwd, git_branch, permission_mode, duration_ms, hook_count, hook_errors,
			prompt_source, user_type, source_tool_assistant_uuid, prompt_id, entrypoint)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET
			uuid=excluded.uuid, parent_uuid=excluded.parent_uuid, session_id=excluded.session_id,
			ts=excluded.ts, type=excluded.type, is_sidechain=excluded.is_sidechain, cwd=excluded.cwd,
			git_branch=excluded.git_branch, permission_mode=excluded.permission_mode,
			duration_ms=excluded.duration_ms, hook_count=excluded.hook_count,
			hook_errors=excluded.hook_errors, prompt_source=excluded.prompt_source,
			user_type=excluded.user_type, source_tool_assistant_uuid=excluded.source_tool_assistant_uuid,
			prompt_id=excluded.prompt_id, entrypoint=excluded.entrypoint`)
	if err != nil {
		return fmt.Errorf("prepare events insert: %w", err)
	}
	defer func() { _ = evStmt.Close() }()
	for _, e := range parsed.events {
		if _, err := evStmt.ExecContext(ctx, path, e.seq, e.uuid, e.parentUUID, e.sessionID, e.ts,
			e.typ, e.isSidechain, e.cwd, e.gitBranch, e.permissionMode, e.durationMs, e.hookCount,
			e.hookErrors, e.promptSource, e.userType, e.sourceToolAssistantUUID, e.promptID,
			e.entrypoint); err != nil {
			return fmt.Errorf("insert event %s#%d: %w", path, e.seq, err)
		}
	}

	callStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(tool_use_id) DO UPDATE SET
			path=excluded.path, seq=excluded.seq, tool_name=excluded.tool_name,
			input_json=excluded.input_json, lead_cmd=excluded.lead_cmd`)
	if err != nil {
		return fmt.Errorf("prepare tool_calls insert: %w", err)
	}
	defer func() { _ = callStmt.Close() }()
	for _, c := range parsed.calls {
		if _, err := callStmt.ExecContext(ctx, c.toolUseID, path, c.seq, c.toolName, c.inputJSON, c.leadCmd); err != nil {
			return fmt.Errorf("insert tool_call %s: %w", c.toolUseID, err)
		}
	}

	resStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len, content, signature)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(tool_use_id) DO UPDATE SET
			path=excluded.path, seq=excluded.seq, is_error=excluded.is_error,
			content_len=excluded.content_len, content=excluded.content, signature=excluded.signature`)
	if err != nil {
		return fmt.Errorf("prepare tool_results insert: %w", err)
	}
	defer func() { _ = resStmt.Close() }()
	for _, r := range parsed.results {
		isErr := 0
		if r.isError {
			isErr = 1
		}
		if _, err := resStmt.ExecContext(ctx, r.toolUseID, path, r.seq, isErr, r.contentLen, r.content, r.signature); err != nil {
			return fmt.Errorf("insert tool_result %s: %w", r.toolUseID, err)
		}
	}

	textStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO assistant_text (path, seq, text) VALUES (?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET text=excluded.text`)
	if err != nil {
		return fmt.Errorf("prepare assistant_text insert: %w", err)
	}
	defer func() { _ = textStmt.Close() }()
	for _, t := range parsed.narration {
		if _, err := textStmt.ExecContext(ctx, path, t.seq, t.text); err != nil {
			return fmt.Errorf("insert assistant_text %s#%d: %w", path, t.seq, err)
		}
	}

	userStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO user_text (path, seq, text_len, text, interrupted) VALUES (?,?,?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET
			text_len=excluded.text_len, text=excluded.text, interrupted=excluded.interrupted`)
	if err != nil {
		return fmt.Errorf("prepare user_text insert: %w", err)
	}
	defer func() { _ = userStmt.Close() }()
	for _, u := range parsed.userText {
		interrupted := 0
		if u.interrupted {
			interrupted = 1
		}
		if _, err := userStmt.ExecContext(ctx, path, u.seq, u.textLen, u.text, interrupted); err != nil {
			return fmt.Errorf("insert user_text %s#%d: %w", path, u.seq, err)
		}
	}

	if withThinking && len(parsed.thinking) > 0 {
		thStmt, err := tx.PrepareContext(ctx, `
			INSERT INTO thinking (path, seq, text) VALUES (?,?,?)
			ON CONFLICT(path, seq) DO UPDATE SET text=excluded.text`)
		if err != nil {
			return fmt.Errorf("prepare thinking insert: %w", err)
		}
		defer func() { _ = thStmt.Close() }()
		for _, t := range parsed.thinking {
			if _, err := thStmt.ExecContext(ctx, path, t.seq, t.text); err != nil {
				return fmt.Errorf("insert thinking %s#%d: %w", path, t.seq, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", path, err)
	}
	return nil
}
