package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Cache policy: detail level, read-through, single flight, provenance and the
// query-scoped list fallback (INV-CACHE-2..5, INV-CACHE-8).

// scriptedBackend is a fake backend whose answers are files in dir, so a test
// can change membership or an entity between calls and count what the
// umbrella actually asked it:
//
//	list-ids.json  the raw wire response for an ids-only list
//	list-full.json the raw wire response for a full list
//	show-<id>.json the raw wire response for show of <id>; absent means not_found
//	show-error     when present, every show answers this error code instead
//	delay          seconds to sleep inside a show (to hold a call in flight)
//	calls.log      one "<op> <id-or-empty>" line per call, appended
type scriptedBackend struct {
	dir  string
	name string
}

func newScriptedBackend(t *testing.T, name string) *scriptedBackend {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
D="` + dir + `"
req=$(cat)
op=$(printf '%s' "$req" | sed -n 's/.*"op":"\([a-z_]*\)".*/\1/p')
id=$(printf '%s' "$req" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
echo "$op $id" >> "$D/calls.log"
case "$op" in
capabilities)
  printf '{"protocolVersion":1,"schemaVersions":{"pr":1,"issue":1},"ops":["capabilities","show","list"]}\n'
  ;;
show)
  [ -f "$D/delay" ] && sleep "$(cat "$D/delay")"
  if [ -f "$D/show-error" ]; then
    printf '{"protocolVersion":1,"schemaVersion":1,"error":{"code":"%s","message":"scripted"}}\n' "$(cat "$D/show-error")"
  elif [ -f "$D/show-$id.json" ]; then
    cat "$D/show-$id.json"
  else
    printf '{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"gone"}}\n'
  fi
  ;;
list)
  if printf '%s' "$req" | grep -q '"ids_only":true'; then cat "$D/list-ids.json"; else cat "$D/list-full.json"; fi
  ;;
*)
  printf '{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unknown_op","message":"no"}}\n'
  ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write scripted backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &scriptedBackend{dir: dir, name: name}
}

func (b *scriptedBackend) set(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(b.dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

// entity is a minimal show payload with as_of at asOf.
func (b *scriptedBackend) setEntity(t *testing.T, id, title string, asOf time.Time) {
	t.Helper()
	b.set(t, "show-"+id+".json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":%q,"title":%q,"state":"open","as_of":%q,"stale":false}}`, id, title, asOf.UTC().Format(time.RFC3339)))
}

func (b *scriptedBackend) setMembership(t *testing.T, ids ...string) {
	t.Helper()
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = strconv.Quote(id)
	}
	b.set(t, "list-ids.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[%s],"cursor":null,"truncated":false}}`, strings.Join(quoted, ",")))
}

// calls returns how many calls of op the backend has received.
func (b *scriptedBackend) calls(t *testing.T, op string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.dir, "calls.log"))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, op+" ") {
			n++
		}
	}
	return n
}

func registryFor(t *testing.T, backend, state string) *Registry {
	t.Helper()
	yaml := "connector:\n  pr:\n    - " + backend + "\n  issue:\n    - " + backend + "\n"
	if state != "" {
		yaml += "state:\n" + state
	}
	reg, err := parseRegistry([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	return reg
}

func servedFields(t *testing.T, resp *scriptout.Response) (servedFrom string, age int64, stale bool) {
	t.Helper()
	var f struct {
		ServedFrom string `json:"served_from"`
		AgeSeconds int64  `json:"age_seconds"`
		Stale      bool   `json:"stale"`
	}
	if err := json.Unmarshal(resp.Result, &f); err != nil {
		t.Fatalf("decode result: %v (%s)", err, resp.Result)
	}
	return f.ServedFrom, f.AgeSeconds, f.Stale
}

func TestCachePutLevel_SummaryNeverDowngradesDetail(t *testing.T) {
	c := newEmptyCache()
	now := time.Now()
	c.PutLevel("a", json.RawMessage(`{"id":"a","detail":true}`), now, now, CacheLevelDetail)
	c.PutLevel("a", json.RawMessage(`{"id":"a","summary":true}`), now.Add(time.Minute), now.Add(time.Minute), CacheLevelSummary)

	got := c.Entries["a"]
	if got.level() != CacheLevelDetail || !strings.Contains(string(got.Content), `"detail":true`) {
		t.Fatalf("a summary write replaced the detail entry: %+v", got)
	}
	if !got.AsOf.Equal(now) {
		t.Fatalf("a summary write moved the detail entry's as_of to %v, want %v", got.AsOf, now)
	}
	// A detail write replaces a summary, and a summary over a tombstone replaces it.
	c.PutLevel("b", json.RawMessage(`{"id":"b","s":1}`), now, now, CacheLevelSummary)
	c.PutLevel("b", json.RawMessage(`{"id":"b","d":1}`), now, now, CacheLevelDetail)
	if c.Entries["b"].level() != CacheLevelDetail {
		t.Fatalf("detail write did not replace summary: %+v", c.Entries["b"])
	}
	c.Remove("a", now)
	c.PutLevel("a", json.RawMessage(`{"id":"a","summary":true}`), now, now, CacheLevelSummary)
	if e := c.Entries["a"]; e.level() != CacheLevelSummary || e.RemovedAt != nil {
		t.Fatalf("summary over a tombstoned detail = %+v, want a live summary", e)
	}
}

func TestCacheGetDetail_RejectsSummaryAndLegacyEntries(t *testing.T) {
	now := time.Now()
	c := &Cache{Entries: map[string]CacheEntry{
		"legacy":  {Content: json.RawMessage(`{"id":"legacy"}`), AsOf: now}, // no level: reads as summary
		"summary": {Content: json.RawMessage(`{"id":"summary"}`), AsOf: now, Level: CacheLevelSummary},
		"detail":  {Content: json.RawMessage(`{"id":"detail"}`), AsOf: now, Level: CacheLevelDetail},
	}}
	for _, id := range []string{"legacy", "summary"} {
		if _, _, ok := c.GetDetail(id, time.Hour, now); ok {
			t.Errorf("GetDetail(%s) hit a non-detail entry", id)
		}
		if _, _, ok := c.Get(id, time.Hour, now); !ok {
			t.Errorf("Get(%s) must still serve it (list fallback)", id)
		}
	}
	if _, _, ok := c.GetDetail("detail", time.Hour, now); !ok {
		t.Error("GetDetail(detail) missed a young detail entry")
	}
	if _, _, ok := c.GetDetail("detail", time.Minute, now.Add(2*time.Minute)); ok {
		t.Error("GetDetail served a detail entry older than maxAge")
	}
}

func TestShowReadThrough_ServedFromCacheWithinTTL(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rt-hit")
	b.setEntity(t, "pr-1", "t", time.Now())
	reg := registryFor(t, b.name, "")
	ctx := context.Background()

	r1, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("first show: %v", err)
	}
	if from, age, stale := servedFields(t, r1); from != "origin" || age != 0 || stale {
		t.Fatalf("first show served_from=%q age=%d stale=%t, want origin/0/false", from, age, stale)
	}
	r2, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("second show: %v", err)
	}
	if from, _, stale := servedFields(t, r2); from != "cache" || stale {
		t.Fatalf("second show served_from=%q stale=%t, want cache/false", from, stale)
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("backend show calls = %d, want 1 (the second read must not reach the backend)", n)
	}
}

func TestShowReadThrough_FreshAndDisabledAndOptOutAlwaysAskTheOrigin(t *testing.T) {
	cases := []struct {
		name  string
		state string
		fresh bool
	}{
		{"fresh flag", "", true},
		{"read ttl off", "  cache_read_ttl: off\n", false},
		{"read ttl zero", "  cache_read_ttl: \"0\"\n", false},
		{"type opted out", "  cache_disabled_types: pr\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			b := newScriptedBackend(t, "backend-rt-bypass")
			b.setEntity(t, "pr-1", "t", time.Now())
			reg := registryFor(t, b.name, tc.state)
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				resp, err := dispatchShow(ctx, reg, "pr", "pr-1", "", tc.fresh)
				if err != nil {
					t.Fatalf("show %d: %v", i, err)
				}
				if from, _, _ := servedFields(t, resp); from != "origin" {
					t.Fatalf("show %d served_from = %q, want origin", i, from)
				}
			}
			if n := b.calls(t, "show"); n != 2 {
				t.Fatalf("backend show calls = %d, want 2", n)
			}
		})
	}
}

func TestShowReadThrough_PastTTLAndSummaryEntriesGoToTheOrigin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rt-miss")
	b.setEntity(t, "old", "t", time.Now())
	b.setEntity(t, "sum", "t", time.Now())
	reg := registryFor(t, b.name, "  cache_read_ttl: 60s\n")
	now := time.Now()
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, &Cache{Entries: map[string]CacheEntry{
		"old": {Content: json.RawMessage(`{"id":"old","as_of":"2020-01-01T00:00:00Z"}`), AsOf: now.Add(-10 * time.Minute), LastAccess: now, Level: CacheLevelDetail},
		"sum": {Content: json.RawMessage(`{"id":"sum","as_of":"2020-01-01T00:00:00Z"}`), AsOf: now, LastAccess: now, Level: CacheLevelSummary},
	}})

	for _, id := range []string{"old", "sum"} {
		resp, err := dispatchShow(context.Background(), reg, "pr", id, "", false)
		if err != nil {
			t.Fatalf("show %s: %v", id, err)
		}
		if from, _, _ := servedFields(t, resp); from != "origin" {
			t.Errorf("show %s served_from = %q, want origin (past ttl / summary-only entry)", id, from)
		}
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls = %d, want 2", n)
	}
}

func TestShowReadThrough_UnavailableFallsBackToStaleDetailOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rt-stale")
	b.set(t, "show-error", "unavailable")
	reg := registryFor(t, b.name, "")
	now := time.Now()
	asOf := now.Add(-30 * time.Minute)
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, &Cache{Entries: map[string]CacheEntry{
		"det": {Content: json.RawMessage(fmt.Sprintf(`{"id":"det","as_of":%q,"stale":false}`, asOf.UTC().Format(time.RFC3339))), AsOf: asOf, LastAccess: now, Level: CacheLevelDetail},
		"sum": {Content: json.RawMessage(`{"id":"sum","as_of":"2020-01-01T00:00:00Z"}`), AsOf: asOf, LastAccess: now, Level: CacheLevelSummary},
	}})

	// read_ttl (120s) has passed, so the origin is asked, answers unavailable,
	// and the stale detail entry is served (INV-CACHE-4, INV-CACHE-1).
	resp, err := dispatchShow(context.Background(), reg, "pr", "det", "", false)
	if err != nil {
		t.Fatalf("show det: %v", err)
	}
	from, age, stale := servedFields(t, resp)
	if from != "cache" || !stale || age < 29*60 || age > 31*60 {
		t.Fatalf("stale fallback served_from=%q age=%d stale=%t, want cache, ~1800s, true", from, age, stale)
	}
	// A summary-level entry must NOT answer a show, even on unavailable.
	if _, err := dispatchShow(context.Background(), reg, "pr", "sum", "", false); err == nil {
		t.Fatal("a summary entry answered a show on unavailable; want the raw unavailable error")
	}
}

func TestShowReadThrough_SingleFlightCollapsesConcurrentReaders(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rt-flight")
	b.setEntity(t, "pr-1", "t", time.Now())
	b.set(t, "delay", "1")
	reg := registryFor(t, b.name, "")

	const readers = 5
	var wg sync.WaitGroup
	froms := make([]string, readers)
	errs := make([]error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := dispatchShow(context.Background(), reg, "pr", "pr-1", "", false)
			errs[i] = err
			if err == nil {
				var f struct {
					ServedFrom string `json:"served_from"`
				}
				_ = json.Unmarshal(resp.Result, &f)
				froms[i] = f.ServedFrom
			}
		}(i)
	}
	wg.Wait()

	origin := 0
	for i := 0; i < readers; i++ {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if froms[i] == "origin" {
			origin++
		}
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("backend show calls = %d across %d concurrent readers, want 1 (single flight)", n, readers)
	}
	if origin != 1 {
		t.Fatalf("%d readers report served_from=origin, want exactly 1 (%v)", origin, froms)
	}
}

func TestShowReadThrough_FreshReaderIsNotServedAnEarlierEntry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rt-freshlock")
	b.setEntity(t, "pr-1", "t", time.Now())
	reg := registryFor(t, b.name, "")
	ctx := context.Background()
	if _, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false); err != nil {
		t.Fatalf("seed show: %v", err)
	}
	// Same second, no contention: --fresh must still call the origin.
	resp, err := dispatchShow(ctx, reg, "pr", "pr-1", "", true)
	if err != nil {
		t.Fatalf("fresh show: %v", err)
	}
	if from, _, _ := servedFields(t, resp); from != "origin" {
		t.Fatalf("--fresh served_from = %q, want origin", from)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls = %d, want 2", n)
	}
}

func TestShowReadThrough_CliCarriesProvenanceAndFreshFlag(t *testing.T) {
	b := newScriptedBackend(t, "backend-rt-cli")
	b.setEntity(t, "pr-1", "t", time.Now())
	writeConfigFor(t, b.name)

	for i, args := range [][]string{{"pr", "show", "pr-1"}, {"pr", "show", "pr-1"}, {"pr", "show", "pr-1", "--fresh"}} {
		stdout, _, code := executePr(t, args)
		if code != 0 {
			t.Fatalf("call %d exit = %d; stdout=%s", i, code, stdout)
		}
		var resp scriptout.Response
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		from, _, _ := servedFields(t, &resp)
		want := []string{"origin", "cache", "origin"}[i]
		if from != want {
			t.Errorf("call %d served_from = %q, want %q", i, from, want)
		}
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls = %d, want 2 (the middle read is a cache hit)", n)
	}
	// The same flag exists on issue show.
	if _, _, code := executePr(t, []string{"issue", "show", "--help"}); code != 0 {
		t.Fatalf("issue show --help exit = %d", code)
	}
}

func TestListFallback_ScopedToTheQueryMembership(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeOpAwareFakeBackend(t, "backend-scope", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","list"]}`)
	reg := registryFor(t, "backend-scope", "")
	now := time.Now()
	entry := func(id string) CacheEntry {
		return CacheEntry{Content: json.RawMessage(fmt.Sprintf(`{"id":%q,"as_of":"2026-01-01T00:00:00Z"}`, id)), AsOf: now, LastAccess: now, Level: CacheLevelSummary}
	}
	seedCache(t, CacheKey{Type: "pr", Backend: "backend-scope"}, &Cache{Entries: map[string]CacheEntry{"a": entry("a"), "b": entry("b"), "c": entry("c")}})
	seedLedger(t, LedgerKey{Type: "pr", Backend: "backend-scope", Query: "mine"}, &Ledger{
		Version: 2,
		Entries: map[string]LedgerEntry{
			"a": {Hash: "h", VersionLastChanged: 1},
			"c": {Hash: "h", VersionLastChanged: 1, RemovedAtVersion: ptrInt64(2)}, // tombstoned: not a live member
		},
	})

	_, ids, ok := cacheFallbackEntities(context.Background(), reg, "pr", "backend-scope", "mine")
	if !ok || len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("fallback for mine = %v ok=%t, want only the live member a", ids, ok)
	}
	if _, ids, ok := cacheFallbackEntities(context.Background(), reg, "pr", "backend-scope", "team"); ok {
		t.Fatalf("fallback for a query with no ledger = %v, want nothing", ids)
	}
}

func ptrInt64(v int64) *int64 { return &v }

// The single-flight lock is exclusive per entity: a second acquirer waits (and
// says so) until the first releases. The 30s fail-open timeout is not
// exercised here.
func TestAcquireShowFlight_LocksAndReleases(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rel1, waited1 := acquireShowFlight(context.Background(), "pr", "x")
	if waited1 {
		t.Fatal("first acquire reported it waited")
	}
	done := make(chan bool, 1)
	go func() {
		rel2, waited2 := acquireShowFlight(context.Background(), "pr", "x")
		rel2()
		done <- waited2
	}()
	select {
	case <-done:
		t.Fatal("second acquire returned while the first still held the lock")
	case <-time.After(200 * time.Millisecond):
	}
	rel1()
	select {
	case waited := <-done:
		if !waited {
			t.Fatal("second acquire did not report that it waited")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second acquire never obtained the lock after release")
	}
	dir, _ := cacheDir()
	if matches, _ := filepath.Glob(filepath.Join(dir, "flight", "pr__*.lock")); len(matches) == 0 {
		t.Fatalf("no flight lock file under %s", dir)
	}
}
