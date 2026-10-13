package logs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// readRows decodes every line of a JSONL file.
func readRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		rows = append(rows, m)
	}
	return rows
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// requestRouter is a router with the event log installed and four handlers:
// annotated (store, stale, 12 s old), plain (attaches nothing), failing
// (unavailable) and slow (sets its own wait).
func requestRouter(t *testing.T, el *EventLog) *router.Router {
	t.Helper()
	r := router.New(router.Options{Instances: []string{"pr", "ci"}})
	r.Use(el.Middleware())
	reg := func(op string, h func(ctx context.Context) (any, error)) {
		r.Register("pr", op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { return h(ctx) }})
	}
	reg("annotated", func(ctx context.Context) (any, error) {
		router.Annotate(ctx, "store", true, 12, 0)
		return "ok", nil
	})
	reg("fresh", func(ctx context.Context) (any, error) {
		router.Annotate(ctx, "origin", false, 0, 0)
		return "ok", nil
	})
	reg("plain", func(context.Context) (any, error) { return "ok", nil })
	reg("failing", func(context.Context) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "origin down")
	})
	reg("slow", func(ctx context.Context) (any, error) {
		router.Annotate(ctx, "origin", false, 0, 250*time.Millisecond)
		return "ok", nil
	})
	return r
}

func dispatch(r *router.Router, instance, op string) scriptout.Response {
	return r.Dispatch(context.Background(), router.Call{Instance: instance, Request: scriptout.Request{Op: op}})
}

func TestMiddlewareWritesOneRequestRowPerRequest(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	el.Now = fixedClock(t0)
	r := requestRouter(t, el)
	for _, op := range []string{"annotated", "plain", "failing", "slow", "fresh"} {
		dispatch(r, "pr", op)
	}
	dispatch(r, "pr", "no_such_op")
	dispatch(r, "elsewhere", "plain_elsewhere")

	rows := readRows(t, filepath.Join(dir, "events.jsonl"))
	if len(rows) != 7 {
		t.Fatalf("one row per request, want 7 got %d: %v", len(rows), rows)
	}
	for i, row := range rows {
		for k, want := range map[string]any{"kind": "request", "service": "pg-connector-github", "version": "v1", "time": "2026-10-13T12:00:00.000Z"} {
			if row[k] != want {
				t.Errorf("row %d: %s = %v, want %v", i, k, row[k], want)
			}
		}
		for _, k := range []string{"op", "instance", "outcome", "wait_ms", "level", "msg", "pid", "duration_ms"} {
			if _, ok := row[k]; !ok {
				t.Errorf("row %d: missing %s: %v", i, k, row)
			}
		}
		if row["pid"].(float64) != float64(os.Getpid()) {
			t.Errorf("row %d: pid %v", i, row["pid"])
		}
	}

	byOp := map[string]map[string]any{}
	for _, row := range rows {
		byOp[row["op"].(string)] = row
	}
	a := byOp["annotated"]
	if a["instance"] != "pr" || a["outcome"] != "ok" || a["served_from"] != "store" || a["stale"] != true || a["age_seconds"] != float64(12) || a["level"] != "info" {
		t.Errorf("annotated row: %v", a)
	}
	if _, ok := a["error_code"]; ok {
		t.Errorf("a success carries no error_code: %v", a)
	}
	// An annotation that says "fresh" still reports stale=false: it was attached.
	if f := byOp["fresh"]; f["served_from"] != "origin" || f["stale"] != false {
		t.Errorf("fresh row: %v", f)
	}
	// A handler that attached nothing leaves served_from and stale out.
	p := byOp["plain"]
	for _, k := range []string{"served_from", "stale", "age_seconds"} {
		if _, ok := p[k]; ok {
			t.Errorf("plain row must omit %s: %v", k, p)
		}
	}
	if p["outcome"] != "ok" || p["wait_ms"] != float64(0) {
		t.Errorf("plain row: %v", p)
	}
	f := byOp["failing"]
	if f["outcome"] != "error" || f["error_code"] != "unavailable" || f["level"] != "error" || !strings.HasSuffix(f["error"].(string), "origin down") || f["msg"] != "failing failed: unavailable" {
		t.Errorf("failing row: %v", f)
	}
	if byOp["slow"]["wait_ms"] != float64(250) {
		t.Errorf("a handler-set wait wins: %v", byOp["slow"])
	}
	u := byOp["no_such_op"]
	if u["outcome"] != "error" || u["error_code"] != "unknown_op" || u["instance"] != "pr" {
		t.Errorf("unknown op row: %v", u)
	}
	if _, ok := u["served_from"]; ok {
		t.Errorf("a refused request attached nothing: %v", u)
	}
}

func TestMiddlewareNeverChangesTheAnswer(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	// The log directory is unwritable: a file where the directory should be.
	el.Path = filepath.Join(dir, "a-file", "events.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "a-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := requestRouter(t, el)
	if resp := dispatch(r, "pr", "plain"); resp.Error != nil || string(resp.Result) != `"ok"` {
		t.Errorf("answer changed by a failed log write: %+v", resp)
	}
	if resp := dispatch(r, "pr", "failing"); resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Errorf("error answer changed: %+v", resp)
	}
}

func TestMiddlewareRecordsDurationFromItsClock(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	ticks := 0
	el.Now = func() time.Time { ticks++; return t0.Add(time.Duration(ticks) * 40 * time.Millisecond) }
	r := requestRouter(t, el)
	dispatch(r, "pr", "plain")
	rows := readRows(t, EventLogPath(dir))
	if rows[0]["duration_ms"] != float64(40) {
		t.Errorf("duration_ms: %v", rows[0])
	}
}

func TestOnSpendWritesOneOriginRowPerObservedSpend(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	el.OnSpend(governor.SpendEvent{At: t0, Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "summary", BatchSize: 74, Cost: 3, Remaining: 4200, Outcome: "ok"})
	el.OnSpend(governor.SpendEvent{At: t0.Add(time.Second), Bucket: governor.BucketRESTCore, Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive, Group: "", BatchSize: 1, Cost: 1, Remaining: 4999, Outcome: "ok"})
	el.OnSpend(governor.SpendEvent{At: t0.Add(2 * time.Second), Bucket: governor.BucketRESTSearch, Purpose: governor.PurposePassthrough, Cost: 1, Remaining: -1, Outcome: "rate_limited"})
	el.OnSpend(governor.SpendEvent{Bucket: governor.BucketGraphQL, Purpose: governor.PurposeFill, Group: "detail", BatchSize: 20, Cost: 0, Remaining: 0})

	rows := readRows(t, EventLogPath(dir))
	if len(rows) != 4 {
		t.Fatalf("one row per observed spend, got %d", len(rows))
	}
	g := rows[0]
	want := map[string]any{
		"kind": "origin", "service": "pg-connector-github", "version": "v1", "time": "2026-10-13T12:00:00.000Z", "level": "info",
		"purpose": "query", "group": "summary", "batch_size": float64(74), "bucket": "graphql",
		"graphql_cost": float64(3), "graphql_remaining": float64(4200), "rest_requests": float64(0), "outcome": "ok",
	}
	for k, v := range want {
		if g[k] != v {
			t.Errorf("graphql row: %s = %v, want %v", k, g[k], v)
		}
	}
	if _, ok := g["rest_remaining"]; ok {
		t.Errorf("a graphql spend carries no rest_remaining: %v", g)
	}
	if _, ok := g["op"]; ok {
		t.Errorf("an origin row is tied to no op: %v", g)
	}
	rest := rows[1]
	for k, v := range map[string]any{"bucket": "rest_core", "purpose": "interactive", "rest_requests": float64(1), "rest_remaining": float64(4999), "graphql_cost": float64(0), "group": ""} {
		if rest[k] != v {
			t.Errorf("rest row: %s = %v, want %v", k, rest[k], v)
		}
	}
	if _, ok := rest["graphql_remaining"]; ok {
		t.Errorf("a rest spend carries no graphql_remaining: %v", rest)
	}
	limited := rows[2]
	if limited["outcome"] != "rate_limited" || limited["level"] != "warn" || limited["bucket"] != "rest_search" {
		t.Errorf("rate-limited row: %v", limited)
	}
	if _, ok := limited["rest_remaining"]; ok {
		t.Errorf("an unknown remaining (-1) is left out: %v", limited)
	}
	// No At: stamped with the log's clock; no outcome: ok; a known remaining of 0 is kept.
	last := rows[3]
	if last["outcome"] != "ok" || last["graphql_remaining"] != float64(0) || last["time"] == "" {
		t.Errorf("defaulted row: %v", last)
	}
}

// The governor itself drives the subscriber: every recorded spend becomes a row.
func TestOnSpendIsTheGovernorsObserver(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	g, err := governor.New(governor.Config{
		GraphQL:    governor.Limits{Cap: 1000},
		RESTCore:   governor.Limits{Cap: 1000},
		RESTSearch: governor.Limits{Cap: 100}, BackgroundShare: 0.9,
	}, governor.Options{OnSpend: el.OnSpend})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "summary", BatchSize: 5, Cost: 7, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, EventLogPath(dir))
	if len(rows) != 1 || rows[0]["graphql_cost"] != float64(7) || rows[0]["group"] != "summary" || rows[0]["batch_size"] != float64(5) {
		t.Fatalf("rows: %v", rows)
	}
}
