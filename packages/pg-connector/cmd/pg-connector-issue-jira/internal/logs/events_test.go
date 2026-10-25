package logs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
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

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// requestRouter is a router with the event log installed and handlers that
// answer ok, unauthenticated, with a pjira 429 text, and unavailable.
func requestRouter(t *testing.T, el *EventLog) *router.Router {
	t.Helper()
	r := router.New(router.Options{Instances: []string{"jira"}})
	r.Use(el.Middleware())
	reg := func(op string, h func(ctx context.Context) (any, error)) {
		r.Register("jira", op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { return h(ctx) }})
	}
	reg("plain", func(context.Context) (any, error) { return "ok", nil })
	reg("annotated", func(ctx context.Context) (any, error) {
		router.Annotate(ctx, "store", true, 12, 0)
		return "ok", nil
	})
	reg("denied", func(context.Context) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrUnauthenticated, "pjira: status 401 Unauthorized")
	})
	reg("throttled", func(context.Context) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: status 429 Too Many Requests")
	})
	reg("down", func(context.Context) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira not found")
	})
	return r
}

func dispatch(r *router.Router, op string) scriptout.Response {
	return r.Dispatch(context.Background(), router.Call{Instance: "jira", Request: scriptout.Request{Op: op}})
}

func TestEventLogNamesAreTheConfigConstantsAndDifferFromTheOldBackend(t *testing.T) {
	dir := t.TempDir()
	if got := filepath.Base(EventLogPath(dir)); got != config.EventLogFileName {
		t.Errorf("event log file %q != config constant %q", got, config.EventLogFileName)
	}
	if got := filepath.Base(DaemonLogPath(dir)); got != config.DaemonLogFileName {
		t.Errorf("daemon log file %q != config constant %q", got, config.DaemonLogFileName)
	}
	if got := filepath.Base(ClientLogPath(dir)); got != config.ClientLogFileName {
		t.Errorf("client log file %q != config constant %q", got, config.ClientLogFileName)
	}
	if ServiceName != "pg-connector-issue-jira-daemon" || ClientServiceName != "pg-connector-issue-jira-daemon-client" {
		t.Errorf("service names: %q %q", ServiceName, ClientServiceName)
	}
	// The stateless backend's service_name is pg-connector-issue-jira; an alert
	// selecting it by equality must never match a daemon row.
	for _, s := range []string{ServiceName, ClientServiceName} {
		if s == "pg-connector-issue-jira" {
			t.Errorf("service name %q collides with the old backend's", s)
		}
	}
}

func TestMiddlewareRequestRowShapesAndFailureClasses(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	el.Now = fixedClock(t0)
	r := requestRouter(t, el)
	for _, op := range []string{"plain", "annotated", "denied", "throttled", "down"} {
		dispatch(r, op)
	}
	rows := readRows(t, filepath.Join(dir, config.EventLogFileName))
	if len(rows) != 5 {
		t.Fatalf("one row per request, want 5 got %d: %v", len(rows), rows)
	}
	byOp := map[string]map[string]any{}
	for _, row := range rows {
		for k, want := range map[string]any{"kind": "request", "service": "pg-connector-issue-jira-daemon", "version": "v1", "time": "2026-10-13T12:00:00.000Z", "instance": "jira"} {
			if row[k] != want {
				t.Errorf("%v: %s = %v, want %v", row["op"], k, row[k], want)
			}
		}
		if row["pid"].(float64) != float64(os.Getpid()) {
			t.Errorf("pid %v", row["pid"])
		}
		byOp[row["op"].(string)] = row
	}

	// Exact key set of a success row: no error and no failure_class keys.
	wantOK := []string{"duration_ms", "instance", "kind", "level", "msg", "op", "outcome", "pid", "service", "time", "version", "wait_ms"}
	if got := keysOf(byOp["plain"]); !reflect.DeepEqual(got, wantOK) {
		t.Errorf("success row keys:\n got %v\nwant %v", got, wantOK)
	}
	if byOp["plain"]["outcome"] != "ok" || byOp["plain"]["level"] != "info" {
		t.Errorf("success row: %v", byOp["plain"])
	}
	if a := byOp["annotated"]; a["served_from"] != "store" || a["stale"] != true || a["age_seconds"] != float64(12) {
		t.Errorf("annotated row: %v", a)
	}

	// Exact key set of an unauthenticated row: failure_class=auth.
	wantErr := []string{"duration_ms", "error", "error_code", "failure_class", "instance", "kind", "level", "msg", "op", "outcome", "pid", "service", "time", "version", "wait_ms"}
	d := byOp["denied"]
	if got := keysOf(d); !reflect.DeepEqual(got, wantErr) {
		t.Errorf("unauthenticated row keys:\n got %v\nwant %v", got, wantErr)
	}
	if d["failure_class"] != "auth" || d["error_code"] != "unauthenticated" || d["outcome"] != "error" || d["msg"] != "denied failed: unauthenticated" {
		t.Errorf("unauthenticated row: %v", d)
	}

	// A 429 text: failure_class=rate_limited under the unavailable code.
	th := byOp["throttled"]
	if got := keysOf(th); !reflect.DeepEqual(got, wantErr) {
		t.Errorf("429 row keys:\n got %v\nwant %v", got, wantErr)
	}
	if th["failure_class"] != "rate_limited" || th["error_code"] != "unavailable" || th["level"] != "error" {
		t.Errorf("429 row: %v", th)
	}

	// An unavailable answer that is not a throttle carries no class (alert b).
	if _, ok := byOp["down"]["failure_class"]; ok {
		t.Errorf("a plain unavailable carries no failure_class: %v", byOp["down"])
	}
	if byOp["down"]["error_code"] != "unavailable" {
		t.Errorf("down row: %v", byOp["down"])
	}
}

func TestMiddlewareNeverChangesTheAnswer(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	el.Path = filepath.Join(dir, "a-file", "events.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "a-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := requestRouter(t, el)
	if resp := dispatch(r, "plain"); resp.Error != nil || string(resp.Result) != `"ok"` {
		t.Errorf("answer changed by a failed log write: %+v", resp)
	}
	if resp := dispatch(r, "denied"); resp.Error == nil || resp.Error.Code != "unauthenticated" {
		t.Errorf("error answer changed: %+v", resp)
	}
}

func TestMiddlewareRecordsDurationFromItsClock(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	ticks := 0
	el.Now = func() time.Time { ticks++; return t0.Add(time.Duration(ticks) * 40 * time.Millisecond) }
	dispatch(requestRouter(t, el), "plain")
	rows := readRows(t, EventLogPath(dir))
	if rows[0]["duration_ms"] != float64(40) {
		t.Errorf("duration_ms: %v", rows[0])
	}
}

func TestOnSpendWritesOneOriginRowPerSpendWithFailureClasses(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(dir, "v1")
	el.OnSpend(governor.SpendEvent{At: t0, Bucket: "jira", Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "summary", BatchSize: 50, Cost: 1, Remaining: 90, Outcome: "ok"})
	el.OnSpend(governor.SpendEvent{At: t0.Add(time.Second), Bucket: "jira", Purpose: governor.PurposeInteractive, BatchSize: 1, Cost: 1, Remaining: -1, Outcome: "unauthenticated"})
	el.OnSpend(governor.SpendEvent{At: t0.Add(2 * time.Second), Bucket: "jira", Purpose: governor.PurposeQuery, Group: "detail", BatchSize: 20, Cost: 1, Remaining: -1, Outcome: "rate_limited"})
	el.OnSpend(governor.SpendEvent{At: t0.Add(3 * time.Second), Bucket: "jira", Purpose: governor.PurposeQuery, Cost: 1, Remaining: -1, Outcome: "error"})
	el.OnSpend(governor.SpendEvent{Bucket: "jira", Purpose: governor.PurposeFill, Group: "detail", BatchSize: 20, Remaining: 0})

	rows := readRows(t, EventLogPath(dir))
	if len(rows) != 5 {
		t.Fatalf("one row per spend, got %d", len(rows))
	}
	ok := rows[0]
	wantKeys := []string{"batch_size", "bucket", "cost", "group", "kind", "level", "msg", "outcome", "pid", "purpose", "remaining", "service", "time", "version"}
	if got := keysOf(ok); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("success origin row keys:\n got %v\nwant %v", got, wantKeys)
	}
	for k, v := range map[string]any{
		"kind": "origin", "service": "pg-connector-issue-jira-daemon", "version": "v1", "time": "2026-10-13T12:00:00.000Z", "level": "info",
		"purpose": "query", "group": "summary", "batch_size": float64(50), "bucket": "jira", "cost": float64(1), "remaining": float64(90), "outcome": "ok",
	} {
		if ok[k] != v {
			t.Errorf("origin row: %s = %v, want %v", k, ok[k], v)
		}
	}
	if _, has := ok["op"]; has {
		t.Errorf("an origin row is tied to no op: %v", ok)
	}

	wantFailKeys := []string{"batch_size", "bucket", "cost", "failure_class", "group", "kind", "level", "msg", "outcome", "pid", "purpose", "service", "time", "version"}
	for i, c := range []struct{ outcome, class string }{{"unauthenticated", "auth"}, {"rate_limited", "rate_limited"}} {
		row := rows[i+1]
		if got := keysOf(row); !reflect.DeepEqual(got, wantFailKeys) {
			t.Errorf("%s origin row keys:\n got %v\nwant %v", c.outcome, got, wantFailKeys)
		}
		if row["outcome"] != c.outcome || row["failure_class"] != c.class || row["level"] != "warn" {
			t.Errorf("%s origin row: %v", c.outcome, row)
		}
	}
	if e := rows[3]; e["outcome"] != "error" || e["level"] != "warn" {
		t.Errorf("error origin row: %v", e)
	} else if _, has := e["failure_class"]; has {
		t.Errorf("a plain error origin row carries no failure_class: %v", e)
	}
	// No At: stamped with the log's clock; no outcome: ok; a remaining of 0 is kept.
	last := rows[4]
	if last["outcome"] != "ok" || last["remaining"] != float64(0) || last["time"] == "" {
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
	if err := g.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "summary", BatchSize: 5, Cost: 7, Outcome: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, EventLogPath(dir))
	if len(rows) != 1 || rows[0]["cost"] != float64(7) || rows[0]["group"] != "summary" || rows[0]["failure_class"] != "rate_limited" {
		t.Fatalf("rows: %v", rows)
	}
}
