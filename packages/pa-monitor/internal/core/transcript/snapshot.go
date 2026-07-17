package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// Snapshot holds all per-session enrichment data extracted in a single pass.
type Snapshot struct {
	FirstPrompt       string
	Model             string
	ContextTokens     int
	TotalTokens       int
	SubagentCount     int
	AwaitingInput     bool
	RateLimitResetsAt time.Time
	// ModelTokens accumulates each token category per model across all
	// non-error assistant usage records (ADR 0021 §6). It is the ingestion the
	// native CostPricer prices; nil/empty when the transcript has no usage.
	// Error (isApiErrorMessage) records are excluded, matching TotalTokens.
	ModelTokens map[string]usage.ModelTokens
	LastError   *ErrorRecord // most recent isApiErrorMessage event in the transcript; nil if no such event seen
	// LastErrorRetryable is pa-monitor's derived auto-resume verdict for
	// LastError (transient server/network → true). It is tracked separately
	// from the shared ErrorRecord because the daemon flips it to false on
	// escalation, independent of the record's intrinsic RetryClass. Zero when
	// LastError is nil.
	LastErrorRetryable bool
}

// maxTranscriptLine bounds a single JSONL line, matching Scan's historical
// bufio.Scanner max-token size. A longer line is an error (the caller then
// treats the transcript as unreadable and uses a zero Snapshot).
const maxTranscriptLine = 16 * 1024 * 1024

// scanEv is the single per-line parse. The `error` field is polymorphic — a
// nested object on system api_error events ({"error":{"error":{"type":...}}})
// and a bare string on synthetic assistant api-error events ("rate_limit") — so
// it is captured as RawMessage and NEVER influences whether a line is skipped;
// each shape is decoded on demand from the raw bytes.
type scanEv struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	Timestamp         time.Time       `json:"timestamp"`
	RetryInMs         int64           `json:"retryInMs"`
	Message           Message         `json:"message"`
	Error             json.RawMessage `json:"error"`
	IsApiErrorMessage bool            `json:"isApiErrorMessage"`
}

// errString returns the synthetic string error kind when `error` is a JSON
// string literal (e.g. "rate_limit"); "" for the object shape or when absent.
func errString(raw json.RawMessage) string {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return ""
}

// nestedErrType extracts the legacy rate_limit_error type from the nested error
// object shape: {"error":{"error":{"type":"..."}}}
func nestedErrType(raw json.RawMessage) string {
	var nested struct {
		Error struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &nested) == nil {
		return nested.Error.Error.Type
	}
	return ""
}

// scanState accumulates all per-session enrichment across the lines fed to it.
// The same state drives both a cold full Scan and the incremental
// ScanIncremental path, so the two paths cannot diverge. The zero value is NOT
// usable (its maps are nil); construct with newScanState.
type scanState struct {
	firstPrompt     string
	firstPromptDone bool

	lastCtxTotal int
	lastCtxModel string
	totalOut     int
	modelTokens  map[string]usage.ModelTokens

	openTasks  map[string]bool
	pendingAUQ map[string]bool

	// (a) rate-limit-reset mechanism (feeds RateLimitResetsAt).
	lastAPIErrTime     time.Time
	lastAPIErrRetry    int64
	hasAPIErr          bool
	resumedAfterAPIErr bool

	// (b) LastError mechanism. lastErrTerminal is the running equivalent of the
	// old tail-walk: it is armed true when an api-error is recorded and cleared
	// when a later user or non-error assistant event is seen.
	hasLastErr      bool
	lastErrTime     time.Time
	lastErrKind     ErrorKind
	lastErrText     string
	lastErrTerminal bool
}

func newScanState() scanState {
	return scanState{
		modelTokens: map[string]usage.ModelTokens{},
		openTasks:   map[string]bool{},
		pendingAUQ:  map[string]bool{},
	}
}

// feed folds a single transcript line (without its trailing newline) into st.
// Unparseable or non-object lines are ignored, matching the historical Scan.
func (st *scanState) feed(line []byte) {
	var ev scanEv
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}

	// A tool_result closes a pending AskUserQuestion or open Task, regardless of
	// which event type carries it.
	for _, b := range ev.Message.Content {
		if b.Type == "tool_result" && b.ToolUseID != "" {
			delete(st.pendingAUQ, b.ToolUseID)
			delete(st.openTasks, b.ToolUseID)
		}
	}

	errKindStr := errString(ev.Error)
	// Only the synthetic-assistant rate-limit shape carries a string error of
	// "rate_limit" with isApiErrorMessage set.
	isSyntheticRateLimit := ev.Type == "assistant" && errKindStr == "rate_limit" && ev.IsApiErrorMessage

	switch ev.Type {
	case "user":
		if !st.firstPromptDone {
			text := plainUserText(ev.Message.Content)
			if cleaned := cleanPromptText(text); cleaned != "" && !strings.HasPrefix(cleaned, "<") {
				st.firstPrompt = cleaned
				st.firstPromptDone = true
			}
		}
		if st.hasAPIErr {
			st.resumedAfterAPIErr = true
		}
		if st.hasLastErr {
			st.lastErrTerminal = false
		}

	case "assistant":
		if isSyntheticRateLimit {
			// Synthetic rate-limit message has zero usage and is NOT a resume.
			// Read the reset time from the text and record it.
			var text string
			for _, b := range ev.Message.Content {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
			if t, ok := ct.ParseRateLimitReset(text, ev.Timestamp); ok {
				st.lastAPIErrTime = t
				st.lastAPIErrRetry = 0 // sentinel: lastAPIErrTime is absolute
				st.hasAPIErr = true
				st.resumedAfterAPIErr = false
			}
		}
		// Track any isApiErrorMessage event (including non-rate-limit kinds) for LastError.
		if ev.IsApiErrorMessage {
			k := ErrorKind(errKindStr)
			switch k {
			case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed, ErrModelNotFound:
				var text string
				for _, c := range ev.Message.Content {
					if c.Type == "text" {
						text = c.Text
						break
					}
				}
				st.hasLastErr = true
				st.lastErrTime = ev.Timestamp
				st.lastErrKind = k
				st.lastErrText = text
				st.lastErrTerminal = true // (re-)arm; a later resume clears it
			}
		}
		if !ev.IsApiErrorMessage {
			u := ev.Message.Usage
			ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if ctx > 0 {
				st.lastCtxTotal = ctx
				st.lastCtxModel = ev.Message.Model
			}
			st.totalOut += u.OutputTokens
			// Cumulative per-model token ingestion for the native CostPricer
			// (ADR 0021 §6).
			if m := ev.Message.Model; m != "" {
				mt := st.modelTokens[m]
				mt.Input += u.InputTokens
				mt.Output += u.OutputTokens
				mt.CacheCreation += u.CacheCreationInputTokens
				mt.CacheRead += u.CacheReadInputTokens
				st.modelTokens[m] = mt
			}
			st.pendingAUQ = map[string]bool{}
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && b.ID != "" {
					switch b.Name {
					case "Task":
						st.openTasks[b.ID] = true
					case "AskUserQuestion":
						st.pendingAUQ[b.ID] = true
					}
				}
			}
			if st.hasAPIErr {
				st.resumedAfterAPIErr = true
			}
			if st.hasLastErr {
				st.lastErrTerminal = false
			}
		}

	case "system":
		if ev.Subtype == "api_error" &&
			nestedErrType(ev.Error) == "rate_limit_error" && ev.RetryInMs > 0 {
			st.lastAPIErrTime = ev.Timestamp
			st.lastAPIErrRetry = ev.RetryInMs
			st.hasAPIErr = true
			st.resumedAfterAPIErr = false
		}
	}
}

// finalize produces the Snapshot from the accumulated state. Maps are copied and
// LastError is freshly allocated so the returned Snapshot never aliases st,
// which may keep being fed on subsequent incremental calls.
func (st *scanState) finalize() Snapshot {
	snap := Snapshot{
		FirstPrompt:   st.firstPrompt,
		Model:         st.lastCtxModel,
		ContextTokens: st.lastCtxTotal,
		TotalTokens:   st.totalOut,
		SubagentCount: len(st.openTasks),
		AwaitingInput: len(st.pendingAUQ) > 0,
	}
	if len(st.modelTokens) > 0 {
		mt := make(map[string]usage.ModelTokens, len(st.modelTokens))
		maps.Copy(mt, st.modelTokens)
		snap.ModelTokens = mt
	}
	if st.hasAPIErr && !st.resumedAfterAPIErr {
		if st.lastAPIErrRetry == 0 {
			// Synthetic shape: lastAPIErrTime is already the absolute reset time.
			snap.RateLimitResetsAt = st.lastAPIErrTime
		} else {
			snap.RateLimitResetsAt = st.lastAPIErrTime.Add(time.Duration(st.lastAPIErrRetry) * time.Millisecond)
		}
	}
	if st.hasLastErr {
		snap.LastError = &ErrorRecord{
			Kind:       st.lastErrKind,
			Text:       st.lastErrText,
			At:         st.lastErrTime,
			IsTerminal: st.lastErrTerminal,
		}
		// Derive the auto-resume verdict from the shared RetryClass policy. Kept
		// on the Snapshot (not the record) so the daemon's escalation flip can
		// override it without mutating the intrinsic classification.
		snap.LastErrorRetryable = Retryable(snap.LastError)
	}
	return snap
}

// foldReader feeds complete newline-terminated lines from r into st, returning
// the number of bytes consumed (including the terminating newlines). A trailing
// segment with no newline is left unconsumed so a half-written final line is
// reprocessed once completed. A single line exceeding maxTranscriptLine is an
// error, matching Scan's historical limit.
func (st *scanState) foldReader(r io.Reader) (int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadBytes('\n')
		if n := len(line); n > 0 && line[n-1] == '\n' {
			if n > maxTranscriptLine {
				return consumed, bufio.ErrTooLong
			}
			consumed += int64(n)
			st.feed(line[:n-1])
		} else if n > maxTranscriptLine {
			return consumed, bufio.ErrTooLong
		}
		if err != nil {
			if err == io.EOF {
				return consumed, nil
			}
			return consumed, err
		}
	}
}

// Accumulator carries incremental scan state for one transcript file across
// polls: the folded scanState, the byte offset just past the last complete line
// consumed, and the file identity used to detect rotation/rewrite. Treat it as
// opaque; obtain one from ScanIncremental and pass it back on the next call.
type Accumulator struct {
	st     scanState
	offset int64
	info   os.FileInfo
	ready  bool
}

func newAccumulator() *Accumulator {
	return &Accumulator{st: newScanState()}
}

// Scan reads path once and returns all enrichment data. It replaces calling
// FirstPrompt, LatestContext, OpenSubagents, IsAwaitingInput, and RateLimitPause
// individually, and is exactly a cold ScanIncremental. Returns a zero Snapshot
// (no error) when path is empty or missing.
func Scan(path string) (Snapshot, error) {
	snap, _, err := ScanIncremental(path, nil)
	return snap, err
}

// ScanIncremental folds only the bytes appended to path since prev and returns
// the resulting Snapshot plus an updated accumulator to pass on the next call.
// It starts fresh (equivalent to a cold Scan) when prev is nil, the file was
// replaced (different inode), truncated below the cached offset, or rewritten in
// place (the byte before the cached offset is no longer a newline). A missing
// file yields a zero Snapshot, a fresh accumulator, and no error (matching Scan).
func ScanIncremental(path string, prev *Accumulator) (Snapshot, *Accumulator, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, newAccumulator(), nil
		}
		return Snapshot{}, nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return Snapshot{}, nil, err
	}

	acc := prev
	fresh := acc == nil || !acc.ready
	if !fresh {
		switch {
		case acc.info == nil || !os.SameFile(acc.info, info):
			fresh = true // rotated / replaced (new inode)
		case info.Size() < acc.offset:
			fresh = true // truncated below what we consumed
		case acc.offset > 0 && !newlineAt(f, acc.offset-1):
			fresh = true // rewritten in place — prefix no longer matches
		}
	}
	if fresh {
		acc = newAccumulator()
	}

	if _, err := f.Seek(acc.offset, io.SeekStart); err != nil {
		return Snapshot{}, nil, err
	}
	n, err := acc.st.foldReader(f)
	if err != nil {
		return Snapshot{}, nil, err
	}
	acc.offset += n
	acc.info = info
	acc.ready = true
	return acc.st.finalize(), acc, nil
}

// newlineAt reports whether the byte at pos is '\n', without disturbing f's
// current read offset (uses ReadAt).
func newlineAt(f *os.File, pos int64) bool {
	var b [1]byte
	if _, err := f.ReadAt(b[:], pos); err != nil {
		return false
	}
	return b[0] == '\n'
}
