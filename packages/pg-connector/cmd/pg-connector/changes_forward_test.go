package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file tests the forward-only mode of the changes verb
// (changes_forward.go) through the real cobra command, against fake backends
// that declare owns_changes and RECORD every request they receive, in order.
// The caller's output writer records into the same log when it is written, so
// the order of "answer written" and "changes_ack sent" is observable.

const (
	fwdEnvelope = `{"protocolVersion":1,"schemaVersion":12,"result":%s}`
	// fwdCapsOwning is the capabilities answer of a daemon-backed backend.
	fwdCapsOwning = `{"protocolVersion":1,"schemaVersions":{"pr":12},"ops":["capabilities","changes","changes_ack"],"vocabulary":{"owns_changes":true,"cache_opt_out":true}}`
	fwdCapsPlain  = `{"protocolVersion":1,"schemaVersions":{"pr":12},"ops":["capabilities","list"]}`
	fwdAckOK      = `{"protocolVersion":1,"schemaVersion":12,"result":{"consumer":"c1","query":"mine","acked_seq":0}}`
	fwdOutputMark = "OUTPUT"
)

func fwdErrorEnvelope(code, message string) string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":12,"error":{"code":%q,"message":%q}}`, code, message)
}

// writeRecordingBackend creates a fake backend that appends every request it
// receives (one JSON line) to logPath and answers by op from byOp; an op not in
// byOp is answered with an unavailable error, so a call that must not happen
// is both logged and visibly wrong.
func writeRecordingBackend(t *testing.T, name, logPath string, byOp map[string]string) {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nreq=$(cat)\nprintf '%s\\n' \"$req\" >> '" + logPath + "'\n")
	sb.WriteString("case \"$req\" in\n")
	for op, resp := range byOp {
		fmt.Fprintf(&sb, "*'\"op\":\"%s\"'*) cat <<'FAKE_BACKEND_EOF'\n%s\nFAKE_BACKEND_EOF\n;;\n", op, resp)
	}
	fmt.Fprintf(&sb, "*) cat <<'FAKE_BACKEND_EOF'\n%s\nFAKE_BACKEND_EOF\n;;\nesac\n", fwdErrorEnvelope("unavailable", "unexpected op"))
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write recording backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// recordedRequest is one line of the request log: a backend request, or the
// output marker the caller's writer appended when the answer was written.
type recordedRequest struct {
	Op   string
	Args map[string]any
}

func readRequestLog(t *testing.T, logPath string) []recordedRequest {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	var out []recordedRequest
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		if line == fwdOutputMark {
			out = append(out, recordedRequest{Op: fwdOutputMark})
			continue
		}
		var req struct {
			Op   string         `json:"op"`
			Args map[string]any `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("decode logged request %q: %v", line, err)
		}
		out = append(out, recordedRequest{Op: req.Op, Args: req.Args})
	}
	return out
}

func opsOf(reqs []recordedRequest) []string {
	ops := make([]string, 0, len(reqs))
	for _, r := range reqs {
		ops = append(ops, r.Op)
	}
	return ops
}

// requestsFor returns the logged requests of op, in order.
func requestsFor(reqs []recordedRequest, op string) []recordedRequest {
	var out []recordedRequest
	for _, r := range reqs {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

func indexOfOp(reqs []recordedRequest, op string) int {
	for i, r := range reqs {
		if r.Op == op {
			return i
		}
	}
	return -1
}

// markingWriter is the caller's output: every Write appends the output marker
// to the request log (so its position among the backend's requests shows when
// the answer was written) and keeps the bytes.
type markingWriter struct {
	buf     bytes.Buffer
	logPath string
}

func (w *markingWriter) Write(p []byte) (int, error) {
	f, err := os.OpenFile(w.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.WriteString(fwdOutputMark + "\n"); err != nil {
		return 0, err
	}
	return w.buf.Write(p)
}

// failingWriter is an output that cannot be written.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// executeChangesLogged runs the root command with out as the caller's output.
func executeChangesLogged(t *testing.T, out interface{ Write([]byte) (int, error) }, args []string) (stderr string, code int, err error) {
	t.Helper()
	root := newRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	var ee *exitError
	switch {
	case err == nil:
		return errBuf.String(), 0, nil
	case errors.As(err, &ee):
		return errBuf.String(), ee.code, err
	default:
		return errBuf.String(), 1, err
	}
}

// runForwardedChanges runs "pr changes" with a marking writer.
func runForwardedChanges(t *testing.T, logPath string, extra ...string) (stdout, stderr string, code int) {
	t.Helper()
	w := &markingWriter{logPath: logPath}
	args := append([]string{"pr", "changes", "--query", "mine", "--consumer", "c1"}, extra...)
	stderr, code, _ = executeChangesLogged(t, w, args)
	return w.buf.String(), stderr, code
}

// fwdRow builds one changes[] row of a daemon answer.
func fwdRow(id string, seq int64, change, kinds, fields string, version int64) string {
	return fmt.Sprintf(`{"change":%q,"source":"pg-connector-pr-github","entity":{"id":%q,"repo":"o/r","number":1,"title":"t","state":"open","version":%d,"head_sha":"abc"},"seq":%d,"kinds":%s,"fields":%s}`,
		change, id, version, seq, kinds, fields)
}

func fwdAnswer(rows []string, nextSeq int64, freshness string) string {
	return fmt.Sprintf(fwdEnvelope, fmt.Sprintf(`{"sources":[{"backend":"pg-connector-pr-github","status":"succeeded","version":5,"truncated":false,"next_seq":%d}],"changes":[%s],"sources_freshness":%s}`,
		nextSeq, strings.Join(rows, ","), freshness))
}

type fwdWire struct {
	Sources []struct {
		Backend   string `json:"backend"`
		Status    string `json:"status"`
		Version   int64  `json:"version"`
		Truncated bool   `json:"truncated"`
		Reason    string `json:"reason"`
		NextSeq   *int64 `json:"next_seq"`
	} `json:"sources"`
	Changes []struct {
		Change string          `json:"change"`
		Source string          `json:"source"`
		Entity json.RawMessage `json:"entity"`
		Seq    *int64          `json:"seq"`
		Kinds  json.RawMessage `json:"kinds"`
		Fields json.RawMessage `json:"fields"`
	} `json:"changes"`
	SourcesFreshness *[]struct {
		Query         string `json:"query"`
		LastSuccessAt string `json:"last_success_at"`
		LastError     string `json:"last_error"`
	} `json:"sources_freshness"`
}

func decodeFwdWire(t *testing.T, stdout string) fwdWire {
	t.Helper()
	var w fwdWire
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("decode forwarded wire: %v (stdout=%s)", err, stdout)
	}
	return w
}

func setupForwarding(t *testing.T, name string, byOp map[string]string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "requests.log")
	if _, ok := byOp["capabilities"]; !ok {
		byOp["capabilities"] = fwdCapsOwning
	}
	if _, ok := byOp["changes_ack"]; !ok {
		byOp["changes_ack"] = fwdAckOK
	}
	writeRecordingBackend(t, name, logPath, byOp)
	writeConfigFor(t, name)
	return logPath
}

func TestForwardedChanges_SendsChangesNeverList_DecodesRows_AcksAfterFlush(t *testing.T) {
	answer := fwdAnswer([]string{
		fwdRow("o/r#1", 41, "added", `["entered_query"]`, `[]`, 3),
		fwdRow("o/r#1", 42, "changed", `["title"]`, `[{"field":"title","before":"a","after":"b"}]`, 4),
	}, 57, `[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z"}]`)
	logPath := setupForwarding(t, "backend-fwd-basic", map[string]string{"changes": answer})

	stdout, stderr, code := runForwardedChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
	}
	reqs := readRequestLog(t, logPath)
	if got := requestsFor(reqs, "list"); len(got) != 0 {
		t.Fatalf("a forwarded call sent list: %v", opsOf(reqs))
	}
	changes := requestsFor(reqs, "changes")
	if len(changes) != 1 {
		t.Fatalf("changes requests = %d, want 1: %v", len(changes), opsOf(reqs))
	}
	want := map[string]any{"consumer": "c1", "query": "mine", "cached": false, "reset": false}
	for k, v := range want {
		if changes[0].Args[k] != v {
			t.Fatalf("changes args[%q] = %v, want %v (args=%v)", k, changes[0].Args[k], v, changes[0].Args)
		}
	}
	if len(changes[0].Args) != len(want) {
		t.Fatalf("changes args = %v, want exactly %v", changes[0].Args, want)
	}

	w := decodeFwdWire(t, stdout)
	if len(w.Changes) != 2 {
		t.Fatalf("changes = %d, want 2: %s", len(w.Changes), stdout)
	}
	if w.Changes[0].Seq == nil || *w.Changes[0].Seq != 41 || w.Changes[1].Seq == nil || *w.Changes[1].Seq != 42 {
		t.Fatalf("seq not carried: %s", stdout)
	}
	if string(w.Changes[0].Kinds) != `[]` && string(w.Changes[0].Kinds) != `["entered_query"]` {
		t.Fatalf("kinds[0] = %s", w.Changes[0].Kinds)
	}
	if string(w.Changes[1].Kinds) != `["title"]` {
		t.Fatalf("kinds[1] = %s, want [\"title\"]", w.Changes[1].Kinds)
	}
	if !strings.Contains(string(w.Changes[1].Fields), `"before":"a"`) || !strings.Contains(string(w.Changes[1].Fields), `"after":"b"`) {
		t.Fatalf("fields[1] = %s", w.Changes[1].Fields)
	}
	if string(w.Changes[0].Fields) != `[]` {
		t.Fatalf("fields[0] = %s, want []", w.Changes[0].Fields)
	}
	if !strings.Contains(string(w.Changes[1].Entity), `"version":4`) || !strings.Contains(string(w.Changes[1].Entity), `"head_sha":"abc"`) {
		t.Fatalf("entity not passed through unchanged: %s", w.Changes[1].Entity)
	}
	if len(w.Sources) != 1 || w.Sources[0].NextSeq == nil || *w.Sources[0].NextSeq != 57 || w.Sources[0].Status != "succeeded" || w.Sources[0].Version != 5 {
		t.Fatalf("sources = %+v", w.Sources)
	}
	if w.SourcesFreshness == nil || len(*w.SourcesFreshness) != 1 || (*w.SourcesFreshness)[0].Query != "mine" || (*w.SourcesFreshness)[0].LastSuccessAt != "2026-03-04T05:06:07Z" {
		t.Fatalf("sources_freshness = %+v", w.SourcesFreshness)
	}

	// The ack is sent once, after the output was written, with the source's
	// own next_seq.
	acks := requestsFor(reqs, "changes_ack")
	if len(acks) != 1 {
		t.Fatalf("changes_ack requests = %d, want 1: %v", len(acks), opsOf(reqs))
	}
	if acks[0].Args["consumer"] != "c1" || acks[0].Args["query"] != "mine" || acks[0].Args["next_seq"] != float64(57) {
		t.Fatalf("ack args = %v", acks[0].Args)
	}
	if out, ack := indexOfOp(reqs, fwdOutputMark), indexOfOp(reqs, "changes_ack"); out < 0 || ack < out {
		t.Fatalf("changes_ack was sent before the output was written: %v", opsOf(reqs))
	}
	if i, j := indexOfOp(reqs, "changes"), indexOfOp(reqs, fwdOutputMark); i < 0 || j < i {
		t.Fatalf("output was written before the backend answered: %v", opsOf(reqs))
	}
}

func TestForwardedChanges_NoRows_StillAcksItsNextSeq_AndWritesEmptyArrays(t *testing.T) {
	answer := fwdAnswer(nil, 99, `[]`)
	logPath := setupForwarding(t, "backend-fwd-quiet", map[string]string{"changes": answer})

	stdout, _, code := runForwardedChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"changes":[]`) || !strings.Contains(stdout, `"sources_freshness":[]`) {
		t.Fatalf("empty arrays must be written as [], never null or omitted: %s", stdout)
	}
	acks := requestsFor(readRequestLog(t, logPath), "changes_ack")
	if len(acks) != 1 || acks[0].Args["next_seq"] != float64(99) {
		t.Fatalf("a quiet poll must still acknowledge its next_seq: %v", acks)
	}
}

func TestForwardedChanges_ResetReplayEntryCarriesSeqZero_AndNullKindsFieldsBecomeArrays(t *testing.T) {
	row := `{"change":"added","source":"pg-connector-pr-github","entity":{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open"},"seq":0,"kinds":null,"fields":null}`
	logPath := setupForwarding(t, "backend-fwd-reset", map[string]string{"changes": fwdAnswer([]string{row}, 7, `[]`)})

	stdout, _, code := runForwardedChanges(t, logPath, "--reset", "--cached")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Changes) != 1 || w.Changes[0].Seq == nil || *w.Changes[0].Seq != 0 {
		t.Fatalf("a reset replay entry must carry seq 0: %s", stdout)
	}
	if string(w.Changes[0].Kinds) != `[]` || string(w.Changes[0].Fields) != `[]` {
		t.Fatalf("kinds and fields must be arrays, never null: kinds=%s fields=%s", w.Changes[0].Kinds, w.Changes[0].Fields)
	}
	changes := requestsFor(readRequestLog(t, logPath), "changes")
	if len(changes) != 1 || changes[0].Args["reset"] != true || changes[0].Args["cached"] != true {
		t.Fatalf("--reset and --cached must be forwarded as reset and cached: %v", changes)
	}
}

func TestForwardedChanges_Unavailable_DegradesSource_NoDelivery_NoAck_NoList(t *testing.T) {
	logPath := setupForwarding(t, "backend-fwd-down", map[string]string{
		"changes": fwdErrorEnvelope("unavailable", "daemon socket /tmp/x.sock refused"),
	})

	stdout, _, code := runForwardedChanges(t, logPath)
	if code == 0 {
		t.Fatalf("exit code = 0, want a degraded fan-out exit code; stdout=%s", stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || !strings.Contains(w.Sources[0].Reason, "daemon socket /tmp/x.sock refused") {
		t.Fatalf("sources = %+v, want one degraded row carrying the reason", w.Sources)
	}
	if len(w.Changes) != 0 {
		t.Fatalf("a failed forward delivered changes: %s", stdout)
	}
	reqs := readRequestLog(t, logPath)
	if len(requestsFor(reqs, "changes_ack")) != 0 {
		t.Fatalf("a source that did not answer was acknowledged: %v", opsOf(reqs))
	}
	if len(requestsFor(reqs, "list")) != 0 {
		t.Fatalf("the ledger diff ran for a failed owning backend: %v", opsOf(reqs))
	}
}

func TestForwardedChanges_CursorExpiredReachesTheCaller(t *testing.T) {
	logPath := setupForwarding(t, "backend-fwd-expired", map[string]string{
		"changes": fwdErrorEnvelope("invalid_argument", "cursor_expired: position fell outside retention; re-run with --reset"),
	})
	stdout, _, code := runForwardedChanges(t, logPath)
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%s", stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || !strings.Contains(w.Sources[0].Reason, "cursor_expired: position fell outside retention; re-run with --reset") {
		t.Fatalf("cursor_expired message must reach the caller intact: %+v", w.Sources)
	}
	if len(requestsFor(readRequestLog(t, logPath), "changes_ack")) != 0 {
		t.Fatal("a cursor_expired source was acknowledged")
	}
}

func TestForwardedChanges_MalformedAnswer_FailsClosed(t *testing.T) {
	two := fmt.Sprintf(fwdEnvelope, `{"sources":[{"backend":"a","status":"succeeded","version":1,"truncated":false,"next_seq":1},{"backend":"b","status":"succeeded","version":1,"truncated":false,"next_seq":2}],"changes":[],"sources_freshness":[]}`)
	logPath := setupForwarding(t, "backend-fwd-malformed", map[string]string{"changes": two})
	stdout, _, code := runForwardedChanges(t, logPath)
	if code == 0 {
		t.Fatalf("exit code = 0 for a malformed answer; stdout=%s", stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || len(w.Changes) != 0 {
		t.Fatalf("malformed answer must degrade the source and deliver nothing: %s", stdout)
	}
	if len(requestsFor(readRequestLog(t, logPath), "changes_ack")) != 0 {
		t.Fatal("a malformed answer was acknowledged")
	}
}

// Decision: the umbrella overwrites the daemon's registry-name stamps with the
// registered backend name, so sources[].backend, every changes[].source and the
// ledger key agree.
func TestForwardedChanges_RegistryNameStamps_AreOverwrittenWithRegisteredName(t *testing.T) {
	answer := fwdAnswer([]string{fwdRow("o/r#1", 1, "added", `["entered_query"]`, `[]`, 1)}, 1, `[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z"}]`)
	logPath := setupForwarding(t, "backend-fwd-named", map[string]string{"changes": answer})
	stdout, _, code := runForwardedChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Backend != "backend-fwd-named" {
		t.Fatalf("sources[].backend = %+v, want the registered name backend-fwd-named", w.Sources)
	}
	if len(w.Changes) != 1 || w.Changes[0].Source != "backend-fwd-named" {
		t.Fatalf("changes[].source = %+v, want the registered name backend-fwd-named", w.Changes)
	}
	if strings.Contains(stdout, "pg-connector-pr-github") {
		t.Fatalf("the daemon's registry name leaked into the output: %s", stdout)
	}
	// The ledger key is the registered name too.
	if l := mustLoadLedger(t, LedgerKey{Type: "pr", Backend: "backend-fwd-named", Query: "mine"}); l.RefreshedAt == nil {
		t.Fatal("the registered-name ledger key was not stamped")
	}
}

func mustLoadLedger(t *testing.T, key LedgerKey) *Ledger {
	t.Helper()
	l, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger %+v: %v", key, err)
	}
	return l
}

func TestForwardedChanges_StampsLedgerFromTheAnswer_NotTheClock_AndKeepsCursorAndIndex(t *testing.T) {
	reported := "2026-03-04T05:06:07Z"
	answer := fwdAnswer([]string{fwdRow("o/r#1", 1, "changed", `["title"]`, `[]`, 2)}, 8,
		fmt.Sprintf(`[{"query":"mine","last_success_at":%q,"last_error":"origin rate limited"},{"query":"other","last_success_at":"2026-01-01T00:00:00Z"}]`, reported))
	logPath := setupForwarding(t, "backend-fwd-stamp", map[string]string{"changes": answer})
	key := LedgerKey{Type: "pr", Backend: "backend-fwd-stamp", Query: "mine"}
	seedAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedLedger(t, key, &Ledger{
		Entries:   map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 2}},
		Version:   3,
		Cursor:    json.RawMessage(`"opaque"`),
		Consumers: map[string]ConsumerState{"c1": {Cursor: 2, LastSeen: seedAt}},
	})

	before := time.Now()
	if _, _, code := runForwardedChanges(t, logPath); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	after := time.Now()

	l := mustLoadLedger(t, key)
	want, _ := time.Parse(time.RFC3339, reported)
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(want) {
		t.Fatalf("refreshed_at = %v, want the backend's %v", l.RefreshedAt, want)
	}
	if l.LastError == nil || l.LastError.Code != "unavailable" || l.LastError.At.Before(before) || l.LastError.At.After(after) {
		t.Fatalf("last_error = %+v, want code unavailable stamped at the call", l.LastError)
	}
	cs := l.Consumers["c1"]
	if cs.LastSeen.Before(before) || cs.LastSeen.After(after) {
		t.Fatalf("last_seen = %v, want within [%v, %v]", cs.LastSeen, before, after)
	}
	if cs.Cursor != 2 {
		t.Fatalf("consumer cursor = %d, want 2 (untouched)", cs.Cursor)
	}
	if l.Version != 3 || len(l.Entries) != 1 || l.Entries["o/r#1"].Hash != "h1" || string(l.Cursor) != `"opaque"` {
		t.Fatalf("entity index, version or cursor touched: %+v", l)
	}
	// Decision: only the polled query's key is stamped; no ledger file is
	// created for a configured query nobody polled.
	otherPath, err := ledgerPath(LedgerKey{Type: "pr", Backend: "backend-fwd-stamp", Query: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(otherPath); err == nil {
		t.Fatalf("a ledger file was created for the unpolled query: %s", otherPath)
	}
}

func TestForwardedChanges_CachedCall_RefreshedAtIsWhatTheBackendReports_NotTheCallTime(t *testing.T) {
	reported := "2026-03-04T05:06:07Z"
	answer := fwdAnswer(nil, 3, fmt.Sprintf(`[{"query":"mine","last_success_at":%q}]`, reported))
	logPath := setupForwarding(t, "backend-fwd-cached", map[string]string{"changes": answer})
	key := LedgerKey{Type: "pr", Backend: "backend-fwd-cached", Query: "mine"}

	if _, _, code := runForwardedChanges(t, logPath, "--cached"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	l := mustLoadLedger(t, key)
	want, _ := time.Parse(time.RFC3339, reported)
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(want) {
		t.Fatalf("refreshed_at = %v after a --cached call, want the backend's %v, not the call's time", l.RefreshedAt, want)
	}
}

func TestForwardedChanges_NoRecordedSuccess_KeepsNoRefreshedAt_AndAbsentEntryStampsOnlyLastSeen(t *testing.T) {
	answer := fwdAnswer(nil, 3, `[{"query":"mine"},{"query":"unrelated","last_success_at":"2026-01-01T00:00:00Z"}]`)
	logPath := setupForwarding(t, "backend-fwd-nosuccess", map[string]string{"changes": answer})
	key := LedgerKey{Type: "pr", Backend: "backend-fwd-nosuccess", Query: "mine"}
	if _, _, code := runForwardedChanges(t, logPath); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	l := mustLoadLedger(t, key)
	if l.RefreshedAt != nil || l.LastError != nil {
		t.Fatalf("a key with no recorded success must keep no refreshed_at or last_error: %+v / %+v", l.RefreshedAt, l.LastError)
	}
	if l.Consumers["c1"].LastSeen.IsZero() {
		t.Fatal("last_seen not stamped")
	}

	// An answer with no entry for the query stamps only last_seen.
	answer2 := fwdAnswer(nil, 4, `[{"query":"unrelated","last_success_at":"2026-01-01T00:00:00Z"}]`)
	logPath2 := setupForwarding(t, "backend-fwd-noentry", map[string]string{"changes": answer2})
	key2 := LedgerKey{Type: "pr", Backend: "backend-fwd-noentry", Query: "mine"}
	if _, _, code := runForwardedChanges(t, logPath2); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	l2 := mustLoadLedger(t, key2)
	if l2.RefreshedAt != nil || l2.Consumers["c1"].LastSeen.IsZero() {
		t.Fatalf("absent entry must stamp only last_seen: %+v", l2)
	}
}

func TestForwardedChanges_LastErrorKeepsItsAtWhileTheFailureIsCurrent(t *testing.T) {
	answer := fwdAnswer(nil, 3, `[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z","last_error":"origin down"}]`)
	logPath := setupForwarding(t, "backend-fwd-since", map[string]string{"changes": answer})
	key := LedgerKey{Type: "pr", Backend: "backend-fwd-since", Query: "mine"}
	since := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC) // newer than the reported success
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{},
		LastError: &LedgerError{At: since, Code: "unavailable"},
	})
	if _, _, code := runForwardedChanges(t, logPath); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	l := mustLoadLedger(t, key)
	if l.LastError == nil || !l.LastError.At.Equal(since) {
		t.Fatalf("last_error = %+v, want the earlier at %v kept (failing since)", l.LastError, since)
	}
}

func TestForwardedChanges_FailedCall_StampsLastSeenAndWireCode_ButNotForCallerStateErrors(t *testing.T) {
	// unavailable: last_seen and last_error code; refreshed_at unchanged.
	logPath := setupForwarding(t, "backend-fwd-failstamp", map[string]string{
		"changes": fwdErrorEnvelope("unavailable", "daemon down"),
	})
	key := LedgerKey{Type: "pr", Backend: "backend-fwd-failstamp", Query: "mine"}
	prior := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{"o/r#9": {Hash: "h", VersionLastChanged: 1}}, Version: 1,
		Consumers:   map[string]ConsumerState{"c1": {Cursor: 1}},
		RefreshedAt: &prior,
	})
	before := time.Now()
	if _, _, code := runForwardedChanges(t, logPath); code == 0 {
		t.Fatal("exit code = 0 for a failed forward")
	}
	l := mustLoadLedger(t, key)
	if l.Consumers["c1"].LastSeen.Before(before) {
		t.Fatalf("last_seen not stamped on a failed call: %v", l.Consumers["c1"].LastSeen)
	}
	if l.Consumers["c1"].Cursor != 1 || l.Version != 1 || len(l.Entries) != 1 {
		t.Fatalf("a failed call touched the cursor or index: %+v", l)
	}
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(prior) {
		t.Fatalf("refreshed_at = %v, want unchanged %v", l.RefreshedAt, prior)
	}
	if l.LastError == nil || l.LastError.Code != "unavailable" {
		t.Fatalf("last_error = %+v, want the wire code unavailable", l.LastError)
	}

	// cursor_expired is invalid_argument: last_seen only, no last_error.
	logPath2 := setupForwarding(t, "backend-fwd-failexpired", map[string]string{
		"changes": fwdErrorEnvelope("invalid_argument", "cursor_expired: re-run with --reset"),
	})
	key2 := LedgerKey{Type: "pr", Backend: "backend-fwd-failexpired", Query: "mine"}
	if _, _, code := runForwardedChanges(t, logPath2); code == 0 {
		t.Fatal("exit code = 0 for cursor_expired")
	}
	l2 := mustLoadLedger(t, key2)
	if l2.LastError != nil {
		t.Fatalf("a caller-state error was recorded as the key's last_error: %+v", l2.LastError)
	}
	if l2.Consumers["c1"].LastSeen.IsZero() {
		t.Fatal("last_seen not stamped on a cursor_expired call")
	}
}

func TestForwardedChanges_WritesNoCacheEntryAndNoTombstone(t *testing.T) {
	removed := fwdRow("o/r#1", 5, "removed", `["removed"]`, `[]`, 6)
	answer := fwdAnswer([]string{fwdRow("o/r#2", 4, "added", `["entered_query"]`, `[]`, 1), removed}, 6, `[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z"}]`)
	logPath := setupForwarding(t, "backend-fwd-nocache", map[string]string{"changes": answer})
	if _, _, code := runForwardedChanges(t, logPath); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, key := range []CacheKey{{Type: "pr", Backend: "backend-fwd-nocache"}} {
		path, err := cachePath(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("a forwarded call wrote a cache file (entry or tombstone): %s", path)
		}
	}
	// And the ledger holds no entity index for the key.
	if l := mustLoadLedger(t, LedgerKey{Type: "pr", Backend: "backend-fwd-nocache", Query: "mine"}); len(l.Entries) != 0 || l.Version != 0 {
		t.Fatalf("a forwarded call indexed entities in the ledger: %+v", l)
	}
}

func TestForwardedChanges_AckFailure_IsReportedOnStderrOnly_AnswerAndExitCodeUnchanged(t *testing.T) {
	answer := fwdAnswer([]string{fwdRow("o/r#1", 1, "added", `["entered_query"]`, `[]`, 1)}, 2, `[]`)
	logPath := setupForwarding(t, "backend-fwd-ackfail", map[string]string{
		"changes":     answer,
		"changes_ack": fwdErrorEnvelope("unavailable", "daemon went away"),
	})
	stdout, stderr, code := runForwardedChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: a failed ack is not a delivery failure; stderr=%s", code, stderr)
	}
	if w := decodeFwdWire(t, stdout); len(w.Changes) != 1 || w.Sources[0].Status != "succeeded" {
		t.Fatalf("the written answer changed: %s", stdout)
	}
	if !strings.Contains(stderr, "acknowledging position 2") || !strings.Contains(stderr, "daemon went away") || !strings.Contains(stderr, "redelivers") {
		t.Fatalf("stderr = %q, want one line naming the failed ack and the redelivery", stderr)
	}
	if n := len(requestsFor(readRequestLog(t, logPath), "changes_ack")); n != 1 {
		t.Fatalf("changes_ack requests = %d, want exactly 1 (no retry)", n)
	}
}

func TestForwardedChanges_OutputWriteFailure_SendsNoAck(t *testing.T) {
	answer := fwdAnswer([]string{fwdRow("o/r#1", 1, "added", `["entered_query"]`, `[]`, 1)}, 2, `[]`)
	logPath := setupForwarding(t, "backend-fwd-writefail", map[string]string{"changes": answer})
	_, _, err := executeChangesLogged(t, failingWriter{}, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if err == nil {
		t.Fatal("a failed output write returned no error")
	}
	if n := len(requestsFor(readRequestLog(t, logPath), "changes_ack")); n != 0 {
		t.Fatalf("changes_ack requests = %d after a failed output write, want 0 (at-least-once)", n)
	}
}

func TestForwardedChanges_OneOwningOneNonOwning_EachTakesItsOwnPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	logOwning := filepath.Join(t.TempDir(), "owning.log")
	logPlain := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-fwd-mix-owning", logOwning, map[string]string{
		"capabilities": fwdCapsOwning,
		"changes":      fwdAnswer([]string{fwdRow("o/r#1", 1, "added", `["entered_query"]`, `[]`, 1)}, 4, `[]`),
		"changes_ack":  fwdAckOK,
	})
	writeRecordingBackend(t, "backend-fwd-mix-plain", logPlain, map[string]string{
		"capabilities": fwdCapsPlain,
		"list":         `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#7","repo":"o/r","number":7,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#7"],"cursor":null,"truncated":false}}`,
	})
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-fwd-mix-owning\n    - backend-fwd-mix-plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	w := &markingWriter{logPath: logOwning}
	_, code, _ := executeChangesLogged(t, w, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, w.buf.String())
	}
	wire := decodeFwdWire(t, w.buf.String())
	if len(wire.Sources) != 2 || len(wire.Changes) != 2 {
		t.Fatalf("wire = %s", w.buf.String())
	}
	var owning, plain int
	for i, s := range wire.Sources {
		switch s.Backend {
		case "backend-fwd-mix-owning":
			owning++
			if s.NextSeq == nil || *s.NextSeq != 4 {
				t.Fatalf("owning source %d lacks next_seq: %+v", i, s)
			}
		case "backend-fwd-mix-plain":
			plain++
			if s.NextSeq != nil {
				t.Fatalf("a ledger-path source carries next_seq: %+v", s)
			}
		}
	}
	if owning != 1 || plain != 1 {
		t.Fatalf("sources = %+v", wire.Sources)
	}
	for _, c := range wire.Changes {
		switch c.Source {
		case "backend-fwd-mix-owning":
			if c.Seq == nil {
				t.Fatalf("forwarded change lacks seq: %+v", c)
			}
		case "backend-fwd-mix-plain":
			if c.Seq != nil || len(c.Kinds) != 0 || len(c.Fields) != 0 {
				t.Fatalf("ledger-path change carries forwarded fields: %+v", c)
			}
		}
	}
	if wire.SourcesFreshness == nil {
		t.Fatalf("sources_freshness missing although one source was forwarded: %s", w.buf.String())
	}

	owningReqs := readRequestLog(t, logOwning)
	if len(requestsFor(owningReqs, "list")) != 0 || len(requestsFor(owningReqs, "changes")) != 1 || len(requestsFor(owningReqs, "changes_ack")) != 1 {
		t.Fatalf("owning backend requests = %v", opsOf(owningReqs))
	}
	plainReqs := readRequestLog(t, logPlain)
	if len(requestsFor(plainReqs, "changes")) != 0 || len(requestsFor(plainReqs, "changes_ack")) != 0 || len(requestsFor(plainReqs, "list")) != 1 {
		t.Fatalf("non-owning backend requests = %v", opsOf(plainReqs))
	}
	// The non-owning backend's ledger path advanced its consumer as before.
	if l := mustLoadLedger(t, LedgerKey{Type: "pr", Backend: "backend-fwd-mix-plain", Query: "mine"}); l.Consumers["c1"].Cursor != l.Version || len(l.Entries) != 1 {
		t.Fatalf("ledger path did not run for the non-owning backend: %+v", l)
	}
}

// Decision (8): a backend whose capabilities call fails has declared nothing
// and keeps the ledger path; its changes op is never called.
func TestForwardedChanges_FailedCapabilities_KeepsTheLedgerPath(t *testing.T) {
	logPath := setupForwarding(t, "backend-fwd-nocaps", map[string]string{
		"capabilities": fwdErrorEnvelope("unavailable", "no capabilities"),
		"list":         `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	})
	stdout, _, code := runForwardedChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	reqs := readRequestLog(t, logPath)
	if len(requestsFor(reqs, "changes")) != 0 || len(requestsFor(reqs, "changes_ack")) != 0 || len(requestsFor(reqs, "list")) != 1 {
		t.Fatalf("requests = %v, want the ledger path (list only)", opsOf(reqs))
	}
	// The wire output of a call in which nothing was forwarded carries none of
	// the forwarded fields.
	for _, forbidden := range []string{`"seq"`, `"kinds"`, `"fields"`, `"next_seq"`, `"sources_freshness"`} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("ledger-path output carries %s: %s", forbidden, stdout)
		}
	}
}

func TestBackendOwnsChanges_PresenceAndBoolConvention(t *testing.T) {
	cases := []struct {
		name  string
		caps  string
		want  bool
		isErr bool
	}{
		{"true", `{"protocolVersion":1,"schemaVersions":{},"ops":[],"vocabulary":{"owns_changes":true}}`, true, false},
		{"false", `{"protocolVersion":1,"schemaVersions":{},"ops":[],"vocabulary":{"owns_changes":false}}`, false, false},
		{"non-bool present", `{"protocolVersion":1,"schemaVersions":{},"ops":[],"vocabulary":{"owns_changes":"yes"}}`, true, false},
		{"absent", `{"protocolVersion":1,"schemaVersions":{},"ops":[],"vocabulary":{"cache_opt_out":true}}`, false, false},
		{"no vocabulary", `{"protocolVersion":1,"schemaVersions":{},"ops":[]}`, false, false},
		{"error", fwdErrorEnvelope("unavailable", "x"), false, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("backend-owns-%d", i)
			writeFakeBackend(t, name, tc.caps)
			writeConfigFor(t, name)
			reg, err := LoadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			got, err := backendOwnsChanges(context.Background(), reg, name)
			if got != tc.want || (err != nil) != tc.isErr {
				t.Fatalf("backendOwnsChanges = (%v, %v), want (%v, err=%v)", got, err, tc.want, tc.isErr)
			}
		})
	}
}
