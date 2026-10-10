package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fanOutTestReg parses a registry whose state: block is stateLine ("" for
// none).
func fanOutTestReg(t *testing.T, stateLine string) *Registry {
	t.Helper()
	doc := "connector: {}\n"
	if stateLine != "" {
		doc += "state:\n  " + stateLine + "\n"
	}
	reg, err := parseRegistry([]byte(doc), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	return reg
}

// fanOutTestNames returns n backend names b0..b(n-1).
func fanOutTestNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("b%d", i)
	}
	return out
}

// settleGoroutines waits briefly for goroutines started by the code under
// test to wind down, returning the final count.
func settleGoroutines(base int) int {
	deadline := time.Now().Add(2 * time.Second)
	n := runtime.NumGoroutine()
	for n > base && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func TestFanOutEach_ResultsInRegistrationOrderUnderReversedCompletion(t *testing.T) {
	names := fanOutTestNames(4)
	// Earlier-registered backends finish LATER.
	delays := map[string]time.Duration{
		"b0": 160 * time.Millisecond,
		"b1": 100 * time.Millisecond,
		"b2": 50 * time.Millisecond,
		"b3": 0,
	}
	var mu sync.Mutex
	var completion []string
	got := fanOutEach(context.Background(), nil, names, func(_ context.Context, b string) string {
		time.Sleep(delays[b])
		mu.Lock()
		completion = append(completion, b)
		mu.Unlock()
		return "v-" + b
	})
	for i, b := range names {
		if got[i].Err != nil || got[i].Val != "v-"+b {
			t.Errorf("slot %d = %+v, want value v-%s in registration order", i, got[i], b)
		}
	}
	if strings.Join(completion, ",") != "b3,b2,b1,b0" {
		t.Fatalf("completion order = %v; the test needs reversed completion to prove ordering", completion)
	}
}

func TestFanOutEach_SlowBackendDoesNotDelayOthers(t *testing.T) {
	names := fanOutTestNames(4)
	const hung = 600 * time.Millisecond
	finished := make([]time.Duration, len(names))
	start := time.Now()
	fanOutEach(context.Background(), nil, names, func(_ context.Context, b string) int {
		if b == "b0" {
			time.Sleep(hung)
		}
		for i, n := range names {
			if n == b {
				finished[i] = time.Since(start)
			}
		}
		return 0
	})
	for i := 1; i < len(names); i++ {
		if finished[i] > hung/2 {
			t.Errorf("backend %s finished after %v: it was held up by the hung b0 (%v)", names[i], finished[i], hung)
		}
	}
	if total := time.Since(start); total < hung || total > 3*hung {
		t.Errorf("total = %v, want about the slowest backend (%v), not the sum", total, hung)
	}
}

func TestFanOutEach_PanicIsolatedToItsBackend(t *testing.T) {
	names := fanOutTestNames(3)
	got := fanOutEach(context.Background(), nil, names, func(_ context.Context, b string) string {
		if b == "b1" {
			panic("boom")
		}
		return "ok-" + b
	})
	if got[0].Val != "ok-b0" || got[0].Err != nil || got[2].Val != "ok-b2" || got[2].Err != nil {
		t.Errorf("siblings were affected by the panic: %+v", got)
	}
	if got[1].Err == nil || !strings.Contains(got[1].Err.Error(), "boom") || !strings.Contains(got[1].Err.Error(), `"b1"`) {
		t.Errorf("panicking backend's Err = %v, want one naming the backend and the panic value", got[1].Err)
	}
}

func TestFanOutEach_CanceledContextSkipsNotYetStartedBackends(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	got := fanOutEach(ctx, nil, fanOutTestNames(3), func(context.Context, string) int {
		calls.Add(1)
		return 1
	})
	if calls.Load() != 0 {
		t.Errorf("fn ran %d times on a canceled ctx, want 0", calls.Load())
	}
	for i, r := range got {
		if !errors.Is(r.Err, context.Canceled) {
			t.Errorf("slot %d Err = %v, want context.Canceled", i, r.Err)
		}
	}
}

func TestFanOutEach_RespectsConcurrencyCap(t *testing.T) {
	reg := fanOutTestReg(t, "fanout_concurrency: 2")
	var cur, peak atomic.Int32
	fanOutEach(context.Background(), reg, fanOutTestNames(6), func(context.Context, string) int {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		return 0
	})
	if peak.Load() != 2 {
		t.Errorf("peak concurrency = %d, want exactly the cap 2", peak.Load())
	}
}

func TestFanOutEach_CapOfOneIsSerialAndInOrder(t *testing.T) {
	reg := fanOutTestReg(t, "fanout_concurrency: 1")
	var order []string
	fanOutEach(context.Background(), reg, fanOutTestNames(4), func(_ context.Context, b string) int {
		order = append(order, b) // unsynchronized on purpose: cap 1 must be single-goroutine
		return 0
	})
	if strings.Join(order, ",") != "b0,b1,b2,b3" {
		t.Errorf("order = %v, want registration order", order)
	}
}

func TestFanOutEach_ZeroBackendsReturnsEmpty(t *testing.T) {
	got := fanOutEach(context.Background(), nil, nil, func(context.Context, string) int { return 1 })
	if len(got) != 0 {
		t.Errorf("got %d results for no backends", len(got))
	}
}

func TestFanOutEach_NoGoroutineLeak(t *testing.T) {
	base := runtime.NumGoroutine()
	for range 20 {
		fanOutEach(context.Background(), nil, fanOutTestNames(5), func(_ context.Context, b string) int {
			if b == "b2" {
				panic("x")
			}
			return 0
		})
	}
	if n := settleGoroutines(base); n > base {
		t.Errorf("goroutines: %d before, %d after; fanOutEach leaked workers", base, n)
	}
}

func TestResolveFanOutConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		want  int
	}{
		{"absent", "", defaultFanOutConcurrency},
		{"set", "fanout_concurrency: 3", 3},
		{"one", "fanout_concurrency: 1", 1},
		{"zero falls back", "fanout_concurrency: 0", defaultFanOutConcurrency},
		{"negative falls back", "fanout_concurrency: -4", defaultFanOutConcurrency},
		{"non-numeric falls back", "fanout_concurrency: lots", defaultFanOutConcurrency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := fanOutTestReg(t, tc.state)
			if got := resolveFanOutConcurrency(reg); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
	if got := resolveFanOutConcurrency(nil); got != defaultFanOutConcurrency {
		t.Errorf("nil registry: got %d, want default", got)
	}
}

// writeSlowFakeBackend is writeFakeBackend with a delay: it sleeps for
// delaySeconds (a sleep(1) argument, "0.3") after reading the request, then
// prints stdout.
func writeSlowFakeBackend(t *testing.T, name, delaySeconds, stdout string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	content := "#!/bin/sh\ncat >/dev/null\nsleep " + delaySeconds + "\ncat <<'FAKE_BACKEND_EOF'\n" + stdout + "\nFAKE_BACKEND_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write slow fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeGatedFakeBackend writes a fake backend that proves concurrency
// without relying on wall-clock margins. On each call it drops
// <dir>/<name>.started, then waits (up to about 5s) until every marker in
// waitFor exists in dir. It answers okOut when they all appeared and
// timeoutOut when the wait ran out, then drops <dir>/<name>.done. Under a
// serial fan-out a backend that waits for a LATER one can never be
// satisfied, so it answers timeoutOut; under a parallel one it answers okOut.
// postSleep ("0.2") delays the answer after the gate, to stagger completion.
func writeGatedFakeBackend(t *testing.T, name, dir string, waitFor []string, okOut, timeoutOut, postSleep string) {
	t.Helper()
	scriptDir := t.TempDir()
	var wait strings.Builder
	for _, w := range waitFor {
		wait.WriteString(`[ -e "` + dir + `/` + w + `" ] || ok=0; `)
	}
	content := "#!/bin/sh\ncat >/dev/null\n" +
		`touch "` + dir + `/` + name + `.started"` + "\n" +
		"i=0\nwhile [ \"$i\" -lt 100 ]; do\n  ok=1; " + wait.String() + "\n  [ \"$ok\" = 1 ] && break\n  i=$((i+1)); sleep 0.05\ndone\n" +
		"sleep " + postSleep + "\n" +
		`touch "` + dir + `/` + name + `.done"` + "\n" +
		"if [ \"$ok\" = 1 ]; then\ncat <<'FAKE_BACKEND_EOF'\n" + okOut + "\nFAKE_BACKEND_EOF\nelse\ncat <<'FAKE_BACKEND_EOF'\n" + timeoutOut + "\nFAKE_BACKEND_EOF\nfi\n"
	if err := os.WriteFile(filepath.Join(scriptDir, name), []byte(content), 0o755); err != nil {
		t.Fatalf("write gated fake backend: %v", err)
	}
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gatedActivity registers the activity backends names, each of which waits
// until every one of them has started (an all-at-once rendezvous) and then
// answers item "<name>-ok" (or "<name>-serial" if the rendezvous never
// happened). post[i] staggers the answers so the LAST registered finishes
// first.
func gatedActivity(t *testing.T, names []string, post []string) {
	t.Helper()
	dir := t.TempDir()
	var started []string
	for _, n := range names {
		started = append(started, n+".started")
	}
	for i, n := range names {
		writeGatedFakeBackend(t, n, dir, started, activityResp("false", n+"-ok"), activityResp("false", n+"-serial"), post[i])
	}
}

// writeActivityConfigWithState is writeActivityListConfig plus a state:
// block.
func writeActivityConfigWithState(t *testing.T, backends []string, stateLine string) {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("activity:\n  sources:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	sb.WriteString("state:\n  " + stateLine + "\n")
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
}

func activityItemIDs(o activityOut) string {
	var ids []string
	for _, it := range o.Items {
		ids = append(ids, it.Source+"/"+it.Item["id"].(string))
	}
	return strings.Join(ids, ",")
}

// TestActivityList_ParallelFanOut_RegistrationOrderUnderReversedCompletion:
// all three sources must be running at the same moment (each waits for the
// other two), the last registered answers first, and rows and items still
// come back in registration order.
func TestActivityList_ParallelFanOut_RegistrationOrderUnderReversedCompletion(t *testing.T) {
	gatedActivity(t, []string{"par-a", "par-b", "par-c"}, []string{"0.5", "0.25", "0"})
	writeActivityListConfig(t, []string{"par-a", "par-b", "par-c"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	o := decodeActivityOut(t, stdout)
	if got := activityItemIDs(o); got != "par-a/par-a-ok,par-b/par-b-ok,par-c/par-c-ok" {
		t.Fatalf("items = %s, want every source running concurrently, in registration order", got)
	}
	for i, want := range []string{"par-a", "par-b", "par-c"} {
		if o.Sources[i]["source"] != want {
			t.Fatalf("sources[%d] = %v, want %s", i, o.Sources[i], want)
		}
	}
}

// TestActivityList_HungSourceDoesNotDelayOthers: the slow source is
// registered FIRST (the serial worst case, every other source queued behind
// it) and does not answer until all four others have finished. Under a
// serial fan-out it would wait for sources that cannot start until it
// returns.
func TestActivityList_HungSourceDoesNotDelayOthers(t *testing.T) {
	dir := t.TempDir()
	others := []string{"hung-b", "hung-c", "hung-d", "hung-e"}
	var othersDone []string
	for _, n := range others {
		othersDone = append(othersDone, n+".done")
		writeGatedFakeBackend(t, n, dir, nil, activityResp("false", n), activityResp("false", n), "0")
	}
	writeGatedFakeBackend(t, "hung-a", dir, othersDone, activityResp("false", "hung-a-ok"), activityResp("false", "hung-a-serial"), "0")
	writeActivityListConfig(t, append([]string{"hung-a"}, others...), nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	want := "hung-a/hung-a-ok,hung-b/hung-b,hung-c/hung-c,hung-d/hung-d,hung-e/hung-e"
	if got := activityItemIDs(decodeActivityOut(t, stdout)); got != want {
		t.Fatalf("items = %s, want %s (the others were queued behind the slow source)", got, want)
	}
}

// TestActivityList_ErrorIsolation: a failing, an undecodable and a
// not-applicable source each yield their own row and never abort or reorder
// the healthy ones, while all run concurrently.
func TestActivityList_ErrorIsolation(t *testing.T) {
	writeSlowFakeBackend(t, "iso-a", "0.4", activityUnavailableResp)
	writeSlowFakeBackend(t, "iso-b", "0.2", `not json at all`)
	writeSlowFakeBackend(t, "iso-c", "0.1", activityUnknownOpResp)
	writeSlowFakeBackend(t, "iso-d", "0", activityResp("false", "d1"))
	writeActivityListConfig(t, []string{"iso-a", "iso-b", "iso-c", "iso-d"}, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout=%s", code, stdout)
	}
	o := decodeActivityOut(t, stdout)
	var got []string
	for _, r := range o.Sources {
		got = append(got, r["source"].(string)+":"+r["status"].(string))
	}
	if strings.Join(got, ",") != "iso-a:degraded,iso-b:degraded,iso-c:disabled,iso-d:succeeded" {
		t.Fatalf("rows = %v", got)
	}
	if activityItemIDs(o) != "iso-d/d1" {
		t.Fatalf("items = %s", activityItemIDs(o))
	}
}

// TestActivityList_ConcurrencyCapOfOneRunsSerially: state.fanout_concurrency
// = 1 restores the serial behavior (and proves the knob is wired through the
// config file).
func TestActivityList_ConcurrencyCapOfOneRunsSerially(t *testing.T) {
	writeSlowFakeBackend(t, "cap-a", "0.5", activityResp("false", "a1"))
	writeSlowFakeBackend(t, "cap-b", "0.5", activityResp("false", "b1"))
	writeActivityConfigWithState(t, []string{"cap-a", "cap-b"}, "fanout_concurrency: 1")

	start := time.Now()
	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	if elapsed < 1000*time.Millisecond {
		t.Fatalf("elapsed %v: cap 1 must run the two 0.5s sources one after the other", elapsed)
	}
	if got := activityItemIDs(decodeActivityOut(t, stdout)); got != "cap-a/a1,cap-b/b1" {
		t.Fatalf("items = %s", got)
	}
}

// TestFanOutAuthStatus_RunsInParallelInRegistrationOrder covers the
// config-free fan-outs (auth status; config validate uses the same helper):
// each backend waits for the other two, so only a concurrent run is healthy.
func TestFanOutAuthStatus_RunsInParallelInRegistrationOrder(t *testing.T) {
	dir := t.TempDir()
	names := []string{"auth-par-a", "auth-par-b", "auth-par-c"}
	started := []string{"auth-par-a.started", "auth-par-b.started", "auth-par-c.started"}
	ok := `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`
	for i, n := range names {
		writeGatedFakeBackend(t, n, dir, started, ok, activityUnavailableResp, []string{"0.4", "0.2", "0"}[i])
	}

	out := FanOutAuthStatus(context.Background(), nil, names)
	var got []string
	for _, s := range out.Sources {
		got = append(got, s.Source+":"+string(s.Status))
	}
	if strings.Join(got, ",") != "auth-par-a:succeeded,auth-par-b:succeeded,auth-par-c:succeeded" {
		t.Fatalf("rows = %v, want all three running at once, in registration order", got)
	}
}

// TestPrChanges_ParallelBackends_OwnLedgersAndRegistrationOrder: three
// backends rendezvous (so they must all be inside their list call at once),
// each refreshes its OWN ledger, and sources and changes come back in
// registration order; a second call reports nothing new (each ledger
// advanced independently).
func TestPrChanges_ParallelBackends_OwnLedgersAndRegistrationOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	gateDir := t.TempDir()
	names := []string{"chg-par-a", "chg-par-b", "chg-par-c"}
	gatedPRBackends(t, gateDir, names)
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - chg-par-a\n    - chg-par-b\n    - chg-par-c\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	var srcs, ids []string
	for _, s := range w.Sources {
		srcs = append(srcs, s.Backend)
	}
	for _, c := range w.Changes {
		var e struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(c.Entity, &e); err != nil {
			t.Fatalf("decode entity %s: %v", c.Entity, err)
		}
		ids = append(ids, c.Source+"/"+e.ID)
	}
	if strings.Join(srcs, ",") != "chg-par-a,chg-par-b,chg-par-c" {
		t.Fatalf("sources = %v", srcs)
	}
	if strings.Join(ids, ",") != "chg-par-a/o/r#1,chg-par-b/o/r#2,chg-par-c/o/r#3" {
		t.Fatalf("changes = %v, want every backend concurrent (ids #1..#3), in registration order", ids)
	}
	for _, b := range names {
		p, perr := ledgerPath(LedgerKey{Type: "pr", Backend: b, Query: "mine"})
		if perr != nil {
			t.Fatalf("ledgerPath(%s): %v", b, perr)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("no ledger written for %s: %v", b, err)
		}
	}
	// The gate markers persist, so the second call's backends pass at once.
	stdout2, _, code2 := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code2 != 0 {
		t.Fatalf("second call exit = %d; stdout=%s", code2, stdout2)
	}
	if w2 := decodeChangesWire(t, stdout2); len(w2.Changes) != 0 {
		t.Fatalf("second call changes = %+v, want none (each ledger advanced)", w2.Changes)
	}
}

// gatedPRBackends registers pr backends that rendezvous like gatedActivity;
// backend i answers entity #(i+1) when the rendezvous happened and #(i+101)
// when it did not (a serial run).
func gatedPRBackends(t *testing.T, gateDir string, names []string) {
	t.Helper()
	list := func(n int) string {
		return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#%d","repo":"o/r","number":%d,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#%d"],"cursor":null,"truncated":false}}`, n, n, n)
	}
	var started []string
	for _, n := range names {
		started = append(started, n+".started")
	}
	for i, n := range names {
		writeGatedFakeBackend(t, n, gateDir, started, list(i+1), list(i+101), []string{"0.4", "0.2", "0"}[i%3])
	}
}

// TestPrList_ParallelBackends_RegistrationOrder: list fans out in parallel
// (the backends rendezvous) while entities and sources stay in registration
// order (the entity-cache writes after each call stay serial, in the fold).
func TestPrList_ParallelBackends_RegistrationOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	names := []string{"plist-a", "plist-b", "plist-c"}
	gatedPRBackends(t, t.TempDir(), names)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - plist-a\n    - plist-b\n    - plist-c\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	var out struct {
		Entities []struct {
			ID string `json:"id"`
		} `json:"entities"`
		Sources []struct {
			Source string `json:"source"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ids, srcs []string
	for _, e := range out.Entities {
		ids = append(ids, e.ID)
	}
	for _, s := range out.Sources {
		srcs = append(srcs, s.Source)
	}
	if strings.Join(ids, ",") != "o/r#1,o/r#2,o/r#3" || strings.Join(srcs, ",") != "plist-a,plist-b,plist-c" {
		t.Fatalf("entities=%v sources=%v, want every backend concurrent, in registration order", ids, srcs)
	}
}
