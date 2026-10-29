// Package ingest reads the tracker's event files into the SQLite database.
//
// Ingest is a Tolerant Reader: kinds it does not know are stored; a line that
// is not valid JSON or lacks a required field is skipped and counted, per file,
// never silently. It is idempotent (event_id is unique) and incremental (the
// byte offset reached in each file is remembered).
package ingest

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/events"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/repo"
)

// FileResult is what one run did with one event file.
type FileResult struct {
	File                string
	LinesRead           int
	EventsNew           int
	EventsDuplicate     int
	SkippedInvalidJSON  int
	SkippedMissingField int
	EventsWithoutCall   int
}

// Skipped is the number of lines skipped (and counted) in the file.
func (r FileResult) Skipped() int { return r.SkippedInvalidJSON + r.SkippedMissingField }

// Result is one ingest run.
type Result struct {
	RunID int64
	Files []FileResult
}

// Run ingests every event file in dir (only names matching the whole file-name
// pattern; everything else is ignored) into db. now stamps the run.
func Run(db *sql.DB, dir string, now time.Time) (*Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, _, ok := events.ParseFileName(e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	res, err := db.Exec("INSERT INTO ingest_runs(started_ms) VALUES (?)", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	runID, _ := res.LastInsertId()
	out := &Result{RunID: runID}
	repoCache := map[string]bool{}
	touched := map[int64]bool{}
	for _, name := range names {
		fr, err := ingestFile(db, runID, dir, name, repoCache, touched)
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		out.Files = append(out.Files, fr)
	}
	if err := settleCalls(db, touched, now); err != nil {
		return out, fmt.Errorf("settlement: %w", err)
	}
	return out, nil
}

func ingestFile(db *sql.DB, runID int64, dir, name string, repoCache map[string]bool, touched map[int64]bool) (FileResult, error) {
	fr := FileResult{File: name}
	session, source, _ := events.ParseFileName(name)

	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return fr, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fr, err
	}

	tx, err := db.Begin()
	if err != nil {
		return fr, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("INSERT OR IGNORE INTO files(file, session_id, source) VALUES (?,?,?)", name, session, source); err != nil {
		return fr, err
	}
	var offset int64
	if err := tx.QueryRow("SELECT read_offset FROM files WHERE file = ?", name).Scan(&offset); err != nil {
		return fr, err
	}
	if st.Size() < offset {
		offset = 0 // file was replaced or truncated: read again; event_id keeps it idempotent
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fr, err
	}

	rd := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // a trailing partial line is left for the next run
			}
			return fr, err
		}
		offset += int64(len(line))
		fr.LinesRead++
		env, raw, kind := parseLine(bytes.TrimSuffix(line, []byte("\n")), session, source)
		switch kind {
		case lineInvalidJSON:
			fr.SkippedInvalidJSON++
			continue
		case lineMissingField:
			fr.SkippedMissingField++
			continue
		}
		isNew, err := store(tx, env, raw, name)
		if err != nil {
			return fr, err
		}
		if !isNew {
			fr.EventsDuplicate++
			continue
		}
		fr.EventsNew++
		if env.ToolUseID == "" {
			fr.EventsWithoutCall++
		} else {
			if _, err := tx.Exec("INSERT OR IGNORE INTO calls(session_id, tool_use_id) VALUES (?,?)", env.SessionID, env.ToolUseID); err != nil {
				return fr, err
			}
			var id int64
			if err := tx.QueryRow("SELECT call_id FROM calls WHERE session_id=? AND tool_use_id=?", env.SessionID, env.ToolUseID).Scan(&id); err != nil {
				return fr, err
			}
			touched[id] = true
		}
		if err := mapRepo(tx, env, repoCache); err != nil {
			return fr, err
		}
	}

	if _, err := tx.Exec("UPDATE files SET read_offset = ? WHERE file = ?", offset, name); err != nil {
		return fr, err
	}
	if fr.LinesRead > 0 {
		if _, err := tx.Exec(`INSERT INTO file_runs(run_id, file, lines_read, events_new, events_duplicate,
			skipped_invalid_json, skipped_missing_field, events_without_call) VALUES (?,?,?,?,?,?,?,?)`,
			runID, name, fr.LinesRead, fr.EventsNew, fr.EventsDuplicate, fr.SkippedInvalidJSON,
			fr.SkippedMissingField, fr.EventsWithoutCall); err != nil {
			return fr, err
		}
	}
	return fr, tx.Commit()
}

type lineClass int

const (
	lineOK lineClass = iota
	lineInvalidJSON
	lineMissingField
)

// parseLine classifies one line. A line that is not a JSON object is invalid
// JSON; an object that fails the envelope rules (a required field missing or
// of the wrong type, an unsupported schema, a source or session that differs
// from the file name's) is a missing-field line.
func parseLine(line []byte, session, source string) (*events.Envelope, []byte, lineClass) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, nil, lineInvalidJSON
	}
	var env events.Envelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, nil, lineMissingField
	}
	if err := env.Validate(); err != nil || env.Source != source || env.SessionID != session {
		return nil, nil, lineMissingField
	}
	var rawData struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &rawData); err != nil {
		return nil, nil, lineMissingField
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, rawData.Data); err != nil {
		return nil, nil, lineMissingField
	}
	return &env, compact.Bytes(), lineOK
}

func store(tx *sql.Tx, env *events.Envelope, rawData []byte, file string) (bool, error) {
	res, err := tx.Exec(`INSERT OR IGNORE INTO events(event_id, session_id, source, kind, ts, tool_use_id, agent_id, data, file)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		env.EventID, env.SessionID, env.Source, env.Kind, env.TS,
		nullable(env.ToolUseID), nullable(env.AgentID), string(rawData), file)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mapRepo records the repo name for the cwd a new tool-call event carries.
func mapRepo(tx *sql.Tx, env *events.Envelope, seen map[string]bool) error {
	if env.Kind != "tracker.call_start" && env.Kind != "tracker.pre_tool" {
		return nil
	}
	cwd, _ := env.Data["cwd"].(string)
	if cwd == "" || seen[cwd] {
		return nil
	}
	seen[cwd] = true
	var have string
	err := tx.QueryRow("SELECT repo FROM cwd_repo WHERE cwd = ?", cwd).Scan(&have)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if name := repo.Name(cwd); name != "" {
		_, err = tx.Exec("INSERT OR IGNORE INTO cwd_repo(cwd, repo) VALUES (?,?)", cwd, name)
		return err
	}
	return nil
}
