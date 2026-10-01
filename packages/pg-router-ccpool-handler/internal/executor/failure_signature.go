package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

// transcriptTailBytes is how much of the end of a session transcript is read
// to classify a failure (INV-CCH-9). The tail is where a failing tool_result
// lands; a bounded read keeps a huge transcript from being slurped.
const transcriptTailBytes = 64 << 10

// dispatchResultKind is the eventlog kind of the per-dispatch failure record
// carrying failure_signature (INV-CCH-9). One record per failed dispatch.
const dispatchResultKind = "dispatch_result"

// noteTranscript remembers the transcript path of the dispatched session from
// a ccpool list row the poll loop already read. Capturing from rows already in
// hand adds no extra List call, and it keeps the path available after ccpool
// has closed or purged the row (INV-CCH-9).
func (r *ccpoolRun) noteTranscript(s ccpool.Session) {
	if s.TranscriptPath == "" {
		return
	}
	r.sigMu.Lock()
	r.transcript = s.TranscriptPath
	r.sigMu.Unlock()
}

// captureSignature classifies the session transcript's tail, once. The first
// call wins, so a call placed BEFORE a teardown step (Close) is not replaced
// by a later call after the transcript may be gone. An empty or unreadable
// transcript path yields unknown with empty evidence.
func (r *ccpoolRun) captureSignature() failsig.Result {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.captured != nil {
		return *r.captured
	}
	res := classifyTranscript(r.transcript)
	r.captured = &res
	return res
}

// classifyTranscript reads the last transcriptTailBytes of the JSONL at path
// and classifies the text of its tool_result/text content. The raw text is
// never retained: only failsig's redacted, bounded Result leaves this function.
func classifyTranscript(path string) failsig.Result {
	unknown := failsig.Result{Signature: failsig.Unknown}
	if path == "" {
		return unknown
	}
	tail, err := readTail(path, transcriptTailBytes)
	if err != nil {
		return unknown
	}
	return failsig.Classify(transcriptText(tail))
}

// readTail returns the last max bytes of the file. When the file is longer
// than max, the first (partial) line is dropped.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var off int64
	if st.Size() > max {
		off = st.Size() - max
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			return nil, nil // one giant partial line: nothing decodable
		}
	}
	return b, nil
}

type tRecord struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type tBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

// transcriptText decodes JSONL records and joins the text of text and
// tool_result content, in order. A line that does not decode is skipped.
func transcriptText(tail []byte) string {
	var sb strings.Builder
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec tRecord
		if json.Unmarshal(line, &rec) != nil || len(rec.Message.Content) == 0 {
			continue
		}
		var s string
		if json.Unmarshal(rec.Message.Content, &s) == nil {
			sb.WriteString(s)
			sb.WriteByte('\n')
			continue
		}
		var blocks []tBlock
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
				sb.WriteByte('\n')
			case "tool_result":
				sb.WriteString(toolResultText(b.Content))
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// toolResultText flattens a tool_result's content (a string or an array of
// {text} blocks).
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []tBlock
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// recordDispatchFailure emits the dispatch_result event for a failed or
// hard-stopped dispatch (INV-CCH-9). werr==nil and context cancellation
// (shutdown, not a session failure) record nothing. A watchdog hard stop is
// budget (the only place budget is set); every other failure uses the
// signature captured from the transcript before teardown. Only the redacted,
// bounded evidence is logged; the raw transcript never is.
func (r *ccpoolRun) recordDispatchFailure(d DispatchContext, name string, werr error) {
	if werr == nil || errors.Is(werr, context.Canceled) || errors.Is(werr, context.DeadlineExceeded) {
		return
	}
	pool := "default"
	if d.Role.CCPool != nil && d.Role.CCPool.PoolDir != "" {
		pool = d.Role.CCPool.PoolDir
	}
	fields := map[string]any{
		"role": d.Role.Name, "pool": pool, "bead": d.Item.ID, "session": name,
	}
	var res failsig.Result
	var be *watchdog.BudgetError
	if errors.As(werr, &be) {
		res = failsig.Classify(be.Error())
		if res.Signature != failsig.Budget {
			res = failsig.Result{Signature: failsig.Budget}
		}
		fields["limit"], fields["used"], fields["cap"] = string(be.Limit), be.Used, be.Cap
	} else {
		res = r.captureSignature()
	}
	fields["failure_signature"] = string(res.Signature)
	fields["signature_evidence"] = res.Evidence
	slog.Warn("dispatch failed", "role", d.Role.Name, "pool", pool, "bead", d.Item.ID,
		"session", name, "failure_signature", string(res.Signature), "signature_evidence", res.Evidence)
	if r.deps.Log != nil {
		_ = r.deps.Log.Emit("error", dispatchResultKind, "dispatch failed", fields)
	}
}
