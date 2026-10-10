// conformance_test.go: pkg/scriptout/conformance's Backend/ExecBackend/driver.Run
// suite run against the REAL COMPILED pg-connector-calendar-task-focus binary,
// with a FAKE pg-task-focus daemon (an httptest server speaking the two reads
// of the daemon's HTTP contract) standing in for the real one, plus end-to-end
// cases for what conformance.Run does not reach (it exercises only unknown-op,
// malformed-stdin and capabilities): the calendar and attention ops, the
// failure mapping, and the event log of a real one-shot process. The fake is
// hand-rolled from the daemon's OpenAPI document; this module never imports the
// daemon's Go packages. Deliberately NOT gated behind a build tag: the suite is
// hermetic (loopback only, no real daemon) so it runs in every `go test ./...`.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// Deadlines are hang guards, not performance assertions: a cold `go build`
// under heavy machine load takes far longer than an idle build.
const (
	buildDeadline = 8 * time.Minute
	runDeadline   = 2 * time.Minute
)

// notesWithSeparator is a notes block as the daemon builds it: the reserved
// cycle_type pair, operator pairs (a repeated key is several values), the
// "---" line, then a note that itself contains a line equal to "---".
const notesWithSeparator = "cycle_type: deep-work\nticket: ABC-1\nticket: ABC-2\n---\nReviewed the migration.\n---\nsecond block\n"

type daemonRequest struct {
	Path, Query, XClient, Host string
}

// fakeDaemon is a stand-in for the pg-task-focus daemon. It records every
// request and answers the two connector reads from its fields.
type fakeDaemon struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []daemonRequest

	attention string // the /attention body
	calendar  string // the /calendar body
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{
		calendar: `{"as_of":"2026-10-10T15:00:00.000Z","stale":false,"calendar_id":"focus-cycles","calendar":"Focus cycles","events":[` +
			`{"id":"ev-open","title":"Deep work","start":"2026-10-10T14:00:00.000Z","end":"2026-10-10T15:00:00.000Z","notes":` + jsonString(notesWithSeparator) + `}]}`,
		attention: `{"as_of":"2026-10-10T15:00:00.000Z","items":[` +
			`{"type":"task","id":"day:2026-10-10:plan","summary":"Plan the day","severity":"high","group":{"key":"daily","label":"Today"},"url":"https://focus.example.test/#/tasks/day:2026-10-10:plan"},` +
			`{"type":"cycle","id":"cyc-1","summary":"Deep work: 3 min over","severity":"medium","group":{"key":"cycles","label":"Cycles"}}]}`,
	}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.requests = append(d.requests, daemonRequest{r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Client"), r.Host})
		cal, att := d.calendar, d.attention
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/calendar":
			q := r.URL.Query()
			if q.Get("from") == "" || q.Get("to") == "" {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"Bad request","status":400,"detail":"The query parameter from is required (an RFC 3339 instant).","reason":"invalid_request","trace_id":"t"}`))
				return
			}
			if name := q.Get("calendar"); name != "" && name != "focus-cycles" && name != "Focus cycles" {
				_, _ = w.Write([]byte(`{"as_of":"2026-10-10T15:00:00.000Z","stale":false,"calendar_id":"focus-cycles","calendar":"Focus cycles","events":[]}`))
				return
			}
			_, _ = w.Write([]byte(cal))
		case "/api/v1/attention":
			_, _ = w.Write([]byte(att))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDaemon) seen() []daemonRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]daemonRequest(nil), d.requests...)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
	buildDir  string
)

// TestMain removes the compiled binary after the package's tests ran.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// buildBinary compiles this package's own real binary via `go build`, once per
// test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pgcctf")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = dir
		bin := filepath.Join(dir, "pg-connector-calendar-task-focus")
		ctx, cancel := context.WithTimeout(context.Background(), buildDeadline)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/pg-connector-calendar-task-focus: %w\n%s", err, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

// call runs the binary once, as the umbrella does: one request on stdin, one
// response on stdout. stateHome keeps the backend's event log in the test's own
// directory.
func call(t *testing.T, bin, stateHome, request string) (stdout []byte, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = strings.NewReader(request)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome, "PG_TASK_FOCUS_ADDR=")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.Bytes(), ee.ExitCode()
	}
	if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return out.Bytes(), 0
}

// response is the wire envelope, with the result left raw.
type response struct {
	Result        json.RawMessage `json:"result"`
	SchemaVersion int             `json:"schemaVersion"`
	Error         *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeResponse(t *testing.T, raw []byte) response {
	t.Helper()
	if err := conformance.CheckResponseBytes(raw); err != nil {
		t.Fatalf("response violates the wire schema: %v\n%s", err, raw)
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConformance_RealBinary_FakeDaemon(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	// ExecBackend spawns bin with no explicit Env, so it inherits this test
	// process's own env: point it at the fake daemon and a scratch state home.
	t.Setenv("PG_TASK_FOCUS_ADDR", strings.TrimPrefix(d.srv.URL, "http://"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	results := conformance.Run(ctx, conformance.ExecBackend{Binary: bin})
	if len(results) == 0 {
		t.Fatal("conformance.Run produced no results at all")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

func TestEndToEnd_ListEvents_SegmentsWithNotesAndIdentity(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	state := t.TempDir()

	out, code := call(t, bin, state, `{"op":"list_events","args":{"start":"2026-10-10T00:00:00Z","end":"2026-10-11T00:00:00Z","calendar":"focus-cycles"},"config":{"base_url":"`+d.srv.URL+`"}}`)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	r := decodeResponse(t, out)
	if r.Error != nil {
		t.Fatalf("error: %+v", r.Error)
	}
	var res struct {
		Entities []struct {
			ID, Title, Start, End, Notes string
			CalendarID                   string `json:"calendar_id"`
			Calendar                     string
			AsOf                         string `json:"as_of"`
			Stale                        bool
		}
		PresentIDs []string `json:"present_ids"`
		Truncated  bool
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Entities) != 1 || res.Truncated {
		t.Fatalf("result = %s", r.Result)
	}
	e := res.Entities[0]
	if e.ID != "ev-open" || e.Title != "Deep work" || e.CalendarID != "focus-cycles" || e.Calendar != "Focus cycles" ||
		e.Start != "2026-10-10T14:00:00.000Z" || e.End != "2026-10-10T15:00:00.000Z" || e.AsOf != "2026-10-10T15:00:00.000Z" || e.Stale {
		t.Errorf("event = %+v", e)
	}
	if e.Notes != notesWithSeparator {
		t.Errorf("notes = %q, want the daemon's block byte for byte", e.Notes)
	}

	reqs := d.seen()
	if len(reqs) != 1 {
		t.Fatalf("daemon requests = %d, want 1", len(reqs))
	}
	q, _ := url.ParseQuery(reqs[0].Query)
	if reqs[0].Path != "/api/v1/calendar" || reqs[0].XClient != "connector" ||
		q.Get("from") != "2026-10-10T00:00:00Z" || q.Get("to") != "2026-10-11T00:00:00Z" || q.Get("calendar") != "focus-cycles" {
		t.Errorf("daemon saw %+v", reqs[0])
	}
}

func TestEndToEnd_ListEvents_OtherCalendarNameIsAnEmptySuccess(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	out, code := call(t, bin, t.TempDir(), `{"op":"list_events","args":{"start":"2026-10-10T00:00:00Z","end":"2026-10-11T00:00:00Z","calendar":"Work"},"config":{"base_url":"`+d.srv.URL+`"}}`)
	r := decodeResponse(t, out)
	if code != 0 || r.Error != nil || !strings.Contains(string(r.Result), `"entities":[]`) {
		t.Errorf("exit %d, response %s", code, out)
	}
}

func TestEndToEnd_List_NamedQueryWindowStartsNow(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	before := time.Now().UTC().Add(-2 * time.Second)
	out, code := call(t, bin, t.TempDir(), `{"op":"list","args":{"query":"ahead"},"config":{"base_url":"`+d.srv.URL+`","queries":{"ahead":["1h","24h"]}}}`)
	r := decodeResponse(t, out)
	if code != 0 || r.Error != nil {
		t.Fatalf("exit %d, response %s", code, out)
	}
	reqs := d.seen()
	if len(reqs) != 1 {
		t.Fatalf("daemon requests = %d", len(reqs))
	}
	q, _ := url.ParseQuery(reqs[0].Query)
	from, err1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	if err1 != nil || err2 != nil {
		t.Fatalf("window %q..%q does not parse: %v %v", q.Get("from"), q.Get("to"), err1, err2)
	}
	if from.Before(before) || from.After(time.Now()) || to.Sub(from) != 24*time.Hour {
		t.Errorf("window = %v..%v, want [now, now+24h)", from, to)
	}
}

func TestEndToEnd_List_UnknownQueryIsQueryNotRecognized(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	out, code := call(t, bin, t.TempDir(), `{"op":"list","args":{"query":"nope"},"config":{"base_url":"`+d.srv.URL+`","queries":{"ahead":"1h"}}}`)
	r := decodeResponse(t, out)
	if r.Error == nil || r.Error.Code != "query_not_recognized" || code != scriptout.ExitCodeForCode("query_not_recognized") {
		t.Errorf("exit %d, response %s", code, out)
	}
	if n := len(d.seen()); n != 0 {
		t.Errorf("daemon requests = %d, want none for an unrecognized query", n)
	}
}

func TestEndToEnd_ListAttention_FeedWithGroupsURLsAndReadOnlyRecovery(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.attention = `{"as_of":"2026-10-10T15:00:00.000Z","items":[` +
		`{"type":"store","id":"store","summary":"pg-task-focus is read-only: the append write failed","severity":"high","group":{"key":"system","label":"System"}},` +
		`{"type":"task","id":"day:2026-10-10:plan","summary":"Plan the day","severity":"medium","group":{"key":"daily","label":"Today"},"url":"https://focus.example.test/#/tasks/day:2026-10-10:plan"}]}`
	d.mu.Unlock()

	out, code := call(t, bin, t.TempDir(), `{"op":"list_attention","config":{"base_url":"`+d.srv.URL+`"}}`)
	r := decodeResponse(t, out)
	if code != 0 || r.Error != nil {
		t.Fatalf("exit %d, response %s", code, out)
	}
	var res []struct {
		Type, ID, Summary, Severity, URL string
		Group                            *struct{ Key, Label string }
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("result is not an item list: %v\n%s", err, r.Result)
	}
	if len(res) != 2 {
		t.Fatalf("items = %d: %s", len(res), r.Result)
	}
	store := res[0]
	if store.Type != "store" || store.Severity != "high" || !strings.Contains(store.Summary, "read-only") ||
		!strings.HasSuffix(store.Summary, "Restart pg-task-focus to recover") || store.Group == nil || store.Group.Key != "system" {
		t.Errorf("store item = %+v", store)
	}
	task := res[1]
	if task.Severity != "medium" || task.URL != "https://focus.example.test/#/tasks/day:2026-10-10:plan" || task.Group == nil || task.Group.Label != "Today" {
		t.Errorf("task item = %+v", task)
	}
	if reqs := d.seen(); len(reqs) != 1 || reqs[0].Path != "/api/v1/attention" || reqs[0].XClient != "connector" {
		t.Errorf("daemon saw %+v", reqs)
	}
}

// A daemon that is down is an unavailable source, never an empty one, and the
// one-shot process still leaves its start and final rows.
func TestEndToEnd_DaemonDown_IsUnavailableAndLeavesStartAndFinalRows(t *testing.T) {
	bin := buildBinary(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close()
	state := t.TempDir()

	for _, op := range []string{"list_attention", "list_events"} {
		args := ""
		if op == "list_events" {
			args = `,"args":{"start":"2026-10-10T00:00:00Z","end":"2026-10-11T00:00:00Z"}`
		}
		out, code := call(t, bin, state, `{"op":"`+op+`"`+args+`,"config":{"base_url":"`+dead+`"}}`)
		r := decodeResponse(t, out)
		if r.Error == nil || r.Error.Code != "unavailable" || code != scriptout.ExitCodeForCode("unavailable") {
			t.Fatalf("%s: exit %d, response %s", op, code, out)
		}
		if !strings.Contains(r.Error.Message, "is it running") {
			t.Errorf("%s: message %q does not tell the operator the daemon may be down", op, r.Error.Message)
		}
	}

	raw, err := os.ReadFile(filepath.Join(state, "pg-connector-calendar-task-focus", "events.jsonl"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("event log has %d lines, want a start and a final row for each of 2 calls:\n%s", len(lines), raw)
	}
	var phases, finals int
	for _, l := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(l), &row); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		if row["phase"] == "start" {
			phases++
		} else if row["error_code"] == "unavailable" && row["failure_stage"] == "connect" {
			finals++
		}
	}
	if phases != 2 || finals != 2 {
		t.Errorf("start rows %d, failed final rows %d, want 2 and 2:\n%s", phases, finals, raw)
	}
}

func TestEndToEnd_ListEvents_BadWindowIsInvalidArgument(t *testing.T) {
	bin := buildBinary(t)
	d := newFakeDaemon(t)
	for name, args := range map[string]string{
		"end before start": `{"start":"2026-10-11T00:00:00Z","end":"2026-10-10T00:00:00Z"}`,
		"not a time":       `{"start":"yesterday","end":"2026-10-10T00:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, code := call(t, bin, t.TempDir(), `{"op":"list_events","args":`+args+`,"config":{"base_url":"`+d.srv.URL+`"}}`)
			r := decodeResponse(t, out)
			if r.Error == nil || r.Error.Code != "invalid_argument" || code != scriptout.ExitCodeForCode("invalid_argument") {
				t.Errorf("exit %d, response %s", code, out)
			}
		})
	}
}

// No credential exists, so `auth status` MUST answer through the unknown_op
// sentinel the umbrella reports as "disabled: not applicable".
func TestEndToEnd_AuthStatusIsUnknownOp(t *testing.T) {
	bin := buildBinary(t)
	out, _ := call(t, bin, t.TempDir(), `{"op":"auth_status"}`)
	r := decodeResponse(t, out)
	if r.Error == nil || r.Error.Code != "unknown_op" {
		t.Errorf("response %s", out)
	}
}
