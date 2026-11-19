package dtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/handlerstate"
)

// ClosedReconcileStampPrefix mirrors the per-pool negative-cache stamp prefix
// the ccpool handler's admission path writes into the lock directory (the
// handler's own test pins the two equal).
const ClosedReconcileStampPrefix = "closed-reconcile--"

// DefaultEvent is a dispatch event for bead zr-w.
const DefaultEvent = `{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}`

// WorkerRole is a minimal ccpool role: the name and the [role.config] a `worker`
// role carries. extraCC is spliced into the ccpool table, extraTop into the top
// level of the config.
func WorkerRole(extraTop, extraCC string) (name string, cfg map[string]any) {
	body := `{"ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello","isolation":{"Type":"none"}` + extraCC + `}` + extraTop + `}`
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		panic(err)
	}
	return "worker", m
}

// Request builds a wire request body: a dispatch (event non-empty) or any other
// request, plus the role object. A nil cfg omits the config member, an empty
// name omits the role member altogether.
func Request(event, name string, cfg map[string]any) string {
	body := map[string]any{"schemaVersion": "1", "id": "d-1"}
	if event != "" {
		body["event"] = json.RawMessage(event)
	}
	if name != "" {
		role := map[string]any{"name": name}
		if cfg != nil {
			role["config"] = cfg
		}
		body["role"] = role
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// LaunchConfig writes a launch config JSON whose handler state directory is a
// temp dir (so the event log is the test's own) and returns its path and that
// state dir. extra is spliced into the top-level object.
func LaunchConfig(t *testing.T, extra string) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	cfgPath = filepath.Join(dir, "launch.json")
	body := `{"minFreeBytes":0,"originProbe":{"stateDir":"` + stateDir + `"}` + extra + `}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir
}

// EventRecords returns the handler event log's records of the given kind.
func EventRecords(t *testing.T, stateDir, kind string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, handlerstate.EventLogName))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["kind"] == kind {
			out = append(out, rec)
		}
	}
	return out
}

// FakeBR is a fake beads.Runner: it returns canned output keyed by joined args,
// or a single error for every call.
type FakeBR struct {
	Out map[string]string
	Err error
}

func (f FakeBR) Run(_ context.Context, args ...string) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	return f.Out[strings.Join(args, " ")], nil
}

// CaptureSlog temporarily replaces slog's default logger with one that
// records every record's message into a slice, runs fn, restores the prior
// default, and returns the captured messages in emission order. Used to observe
// the sweep passes' log lines without depending on os.Stderr pipe timing.
func CaptureSlog(t *testing.T, fn func()) []string {
	t.Helper()
	rec := &recordingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(old)
	fn()
	rec.Mu.Lock()
	defer rec.Mu.Unlock()
	out := make([]string, len(rec.messages))
	copy(out, rec.messages)
	return out
}

type recordingHandler struct {
	Mu       sync.Mutex
	messages []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

var OrphanNowT = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// OrphanCC is a ccpool.Runner whose rows really change: Close(non-purge) turns
// a row into a handler-closed one and SetMeta upserts meta, like ccpool does.
type OrphanCC struct {
	Mu       sync.Mutex
	Sessions []ccpool.Session
	ListErr  error
	Closes   []string
	Purges   []bool
	MetaSets [][3]string
	OnClose  func()
}

func (c *OrphanCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}
func (c *OrphanCC) Send(context.Context, string, string, ccpool.SendMode) error { return nil }
func (c *OrphanCC) Cancel(context.Context, string) error                        { return nil }
func (c *OrphanCC) Capacity(context.Context) (ccpool.Capacity, error) {
	return ccpool.Capacity{Free: 1}, nil
}

func (c *OrphanCC) List(context.Context) ([]ccpool.Session, error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if c.ListErr != nil {
		return nil, c.ListErr
	}
	out := make([]ccpool.Session, len(c.Sessions))
	for i, s := range c.Sessions {
		m := make(map[string]string, len(s.Meta))
		for k, v := range s.Meta {
			m[k] = v
		}
		s.Meta = m
		out[i] = s
	}
	return out, nil
}

func (c *OrphanCC) Close(_ context.Context, id string, purge bool) error {
	if c.OnClose != nil {
		c.OnClose()
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.Closes = append(c.Closes, id)
	c.Purges = append(c.Purges, purge)
	for i := range c.Sessions {
		if c.Sessions[i].ExternalID == id {
			c.Sessions[i].CloseReason, c.Sessions[i].Live = "handler", false
		}
	}
	return nil
}

func (c *OrphanCC) SetMeta(_ context.Context, id, key, value string) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.MetaSets = append(c.MetaSets, [3]string{id, key, value})
	for i := range c.Sessions {
		if c.Sessions[i].ExternalID == id {
			if c.Sessions[i].Meta == nil {
				c.Sessions[i].Meta = map[string]string{}
			}
			c.Sessions[i].Meta[key] = value
		}
	}
	return nil
}

func (c *OrphanCC) Closed() []string {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return append([]string(nil), c.Closes...)
}

// OrphanBD serves `show` from a status/assignee table and records every other
// call. ShowErr makes every show fail.
type OrphanBD struct {
	Mu      sync.Mutex
	Issues  map[string]string // bead id -> raw show JSON
	ShowErr error
	Calls   []string
}

func (b *OrphanBD) Run(_ context.Context, args ...string) (string, error) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if len(args) > 0 && args[0] == "show" {
		b.Calls = append(b.Calls, strings.Join(args, " "))
		if b.ShowErr != nil {
			return "", b.ShowErr
		}
		return b.Issues[args[1]], nil
	}
	b.Calls = append(b.Calls, strings.Join(args, " "))
	return "", nil
}

func (b *OrphanBD) Writes() []string {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	var w []string
	for _, c := range b.Calls {
		if !strings.HasPrefix(c, "show ") {
			w = append(w, c)
		}
	}
	return w
}

func (b *OrphanBD) Shows() int {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	n := 0
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "show ") {
			n++
		}
	}
	return n
}

// SetSession edits the one row under the fake pool's lock, as ccpool changing
// under the reconcile would.
func (c *OrphanCC) SetSession(mut func(*ccpool.Session)) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	mut(&c.Sessions[0])
}

// CallsCopy returns a copy of every bd call recorded, under the lock.
func (b *OrphanBD) CallsCopy() []string {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return append([]string(nil), b.Calls...)
}
