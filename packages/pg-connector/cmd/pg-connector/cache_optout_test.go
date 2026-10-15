package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// A backend that declares cache_opt_out holds its own cache and freshness
// (INV-CACHE-9): the umbrella passes its answer annotations through unchanged
// and keeps no cache entry, lock file or tombstone for it. A backend that does
// not declare it keeps every rule of the umbrella cache (INV-CACHE-1).

// capsCacheOptOut is a capabilities answer declaring cache_opt_out but NOT
// owns_changes, so a changes call against it takes the ledger path.
const (
	capsCacheOptOutPR = `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show","list","files","commits","review_pending"],"vocabulary":{"cache_opt_out":true}}`
	capsCacheOptOutCI = `{"protocolVersion":1,"schemaVersions":{"ci":1},"ops":["capabilities","list_runs"],"vocabulary":{"cache_opt_out":true}}`
	capsPlainPR       = `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show","list","files","commits","review_pending"]}`
)

// optOutAnnotations is the annotation set the backend answers with in these
// tests: a stale answer served from the backend's own cache, 900 seconds old,
// with two field groups.
const optOutAnnotations = `"served_from":"cache","stale":true,"age_seconds":900,"groups":[{"group":"summary","fetched_at":"2026-01-01T00:00:00Z","fresh_until":"2026-01-01T00:02:00Z","max_age_at":"2026-01-01T01:00:00Z"},{"group":"checks","fetched_at":"2026-01-01T00:01:00Z","fresh_until":"2026-01-01T00:03:00Z","last_error":"unavailable"}]`

// cacheFilesFor lists every file under the XDG state dir whose path names
// backend: the entity cache file, its lock file or a temp file of either.
func cacheFilesFor(t *testing.T, backend string) []string {
	t.Helper()
	var found []string
	root := os.Getenv("XDG_STATE_HOME")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(path, backend) && strings.Contains(path, string(filepath.Separator)+cacheDirName+string(filepath.Separator)) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

func requireNoCacheFiles(t *testing.T, backend string) {
	t.Helper()
	if files := cacheFilesFor(t, backend); len(files) != 0 {
		t.Fatalf("cache files for opted-out backend %q = %v, want none", backend, files)
	}
}

// decodeJSONValue decodes raw into a generic value for a semantic comparison.
func decodeJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestOptOutShow_PassesBackendAnnotationsThroughAndAsksTheBackendEveryTime(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-optout-show")
	b.set(t, "cache-opt-out", "")
	answer := `{"id":"pr-1","title":"t","state":"open","as_of":"2026-01-01T00:00:00Z",` + optOutAnnotations + `}`
	b.set(t, "show-pr-1.json", `{"protocolVersion":1,"schemaVersion":1,"result":`+answer+`}`)
	reg := registryFor(t, b.name, "")
	ctx := context.Background()

	r1, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("first show: %v", err)
	}
	if got, want := decodeJSONValue(t, r1.Result), decodeJSONValue(t, []byte(answer)); !reflect.DeepEqual(got, want) {
		t.Fatalf("first show result = %s, want the backend's answer unchanged: %s", r1.Result, answer)
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("backend show calls after the first show = %d, want exactly 1", n)
	}
	requireNoCacheFiles(t, b.name)

	// No read-through: a second read of the same entity asks the backend again.
	r2, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("second show: %v", err)
	}
	if got, want := decodeJSONValue(t, r2.Result), decodeJSONValue(t, []byte(answer)); !reflect.DeepEqual(got, want) {
		t.Fatalf("second show result = %s, want the backend's answer unchanged", r2.Result)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls after the second show = %d, want 2 (no read-through)", n)
	}
	requireNoCacheFiles(t, b.name)
}

func TestOptOutShow_FreshAnswerOfABackendThatDoesNotOptOutIsAnnotatedAsToday(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-nooptout-show")
	answer := `{"id":"pr-1","title":"t","state":"open","as_of":"2026-01-01T00:00:00Z",` + optOutAnnotations + `}`
	b.set(t, "show-pr-1.json", `{"protocolVersion":1,"schemaVersion":1,"result":`+answer+`}`)
	reg := registryFor(t, b.name, "")

	resp, err := dispatchShow(context.Background(), reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if from, age, _ := servedFields(t, resp); from != servedFromOrigin || age != 0 {
		t.Fatalf("served_from=%q age_seconds=%d, want origin/0 (the umbrella overwrites a non-opted-out backend's annotations)", from, age)
	}
	if files := cacheFilesFor(t, b.name); len(files) == 0 {
		t.Fatal("no cache file written for a backend that does not opt out; the umbrella cache must stay on for it")
	}
}

func TestOptOutShow_UnavailableStaysUnavailableEvenWithAnOldCacheFileOnDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-optout-unavail")
	b.set(t, "cache-opt-out", "")
	b.set(t, "show-error", "unavailable")
	reg := registryFor(t, b.name, "")
	now := time.Now()
	asOf := now.Add(-5 * time.Minute)
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, &Cache{Entries: map[string]CacheEntry{
		"pr-1": {Content: json.RawMessage(`{"id":"pr-1","as_of":"` + asOf.UTC().Format(time.RFC3339) + `"}`), AsOf: asOf, LastAccess: now, Level: CacheLevelDetail},
	}})

	resp, err := dispatchShow(context.Background(), reg, "pr", "pr-1", "", false)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("show err = %v (resp %+v), want an unavailable error with no stale content served", err, resp)
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("backend show calls = %d, want 1 (the entry on disk must not satisfy a read-through)", n)
	}
}

func TestOptOutShow_FailedCapabilitiesCallKeepsTheCacheEnabled(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-optout-capsfail")
	b.set(t, "caps-fail", "")
	b.setEntity(t, "pr-1", "t", time.Now())
	reg := registryFor(t, b.name, "")
	ctx := context.Background()

	if _, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false); err != nil {
		t.Fatalf("first show: %v", err)
	}
	r2, err := dispatchShow(ctx, reg, "pr", "pr-1", "", false)
	if err != nil {
		t.Fatalf("second show: %v", err)
	}
	if from, _, _ := servedFields(t, r2); from != servedFromCache {
		t.Fatalf("second show served_from = %q, want cache (a failed capabilities call must not read as an opt-out)", from)
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("backend show calls = %d, want 1", n)
	}
}

func TestOptOutList_PrListPassesAnnotationsThroughAndWritesNoCache(t *testing.T) {
	prAnswer := `{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"as_of":"2026-01-01T00:00:00Z",` + optOutAnnotations + `}`
	writeOpAwareFakeBackend(t, "backend-optout-prlist", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[` + prAnswer + `],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, capsCacheOptOutPR)
	writeConfigFor(t, "backend-optout-prlist")

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var outcome struct {
		Entities []json.RawMessage `json:"entities"`
	}
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil || len(outcome.Entities) != 1 {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	assertAnnotationsUnchanged(t, outcome.Entities[0])
	requireNoCacheFiles(t, "backend-optout-prlist")
}

func TestOptOutList_PrListUnavailableServesNoCachedContentEvenWithAnOldCacheFile(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-optout-prlist-down", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, capsCacheOptOutPR)
	writeConfigFor(t, "backend-optout-prlist-down")
	key := CacheKey{Type: "pr", Backend: "backend-optout-prlist-down"}
	now := time.Now()
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: json.RawMessage(`{"id":"o/r#1","as_of":"2026-01-01T00:00:00Z"}`), AsOf: now, LastAccess: now, Level: CacheLevelSummary},
	}})
	seedLedger(t, LedgerKey{Type: "pr", Backend: key.Backend, Query: "mine", Instance: ledgerInstanceDiscriminator("pr")}, &Ledger{
		Entries: map[string]LedgerEntry{"o/r#1": {Hash: "h", VersionLastChanged: 1}},
		Version: 1,
	})

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (the only source is degraded); stdout=%s", code, stdout)
	}
	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 0 {
		t.Fatalf("entities = %+v, want none: no cached content for an opted-out backend", outcome.Entities)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Reason == cacheFallbackReason {
		t.Fatalf("sources = %+v, want one row that is not the cache-fallback row", outcome.Sources)
	}
}

func TestOptOutList_CiListPassesAnnotationsThroughAndWritesNoCache(t *testing.T) {
	run := `{"id":"run-1","name":"build","status":"completed","conclusion":"success","url":"u","provider":"github-actions","head_sha":"deadbeef","repo":"o/r","pr_id":"pr-1","as_of":"2026-01-01T00:00:00Z",` + optOutAnnotations + `}`
	writeOpAwareFakeBackend(t, "backend-optout-cilist", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[` + run + `]}`,
	}, capsCacheOptOutCI)
	writeCiConfigFor(t, "backend-optout-cilist")

	stdout, _, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var outcome struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil || len(outcome.Runs) != 1 {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	assertAnnotationsUnchanged(t, outcome.Runs[0])
	requireNoCacheFiles(t, "backend-optout-cilist")
}

func TestOptOutList_CiListUnavailableServesNoCachedContentEvenWithAnOldCacheFile(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-optout-cilist-down", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, capsCacheOptOutCI)
	writeCiConfigFor(t, "backend-optout-cilist-down")
	seedCache(t, CacheKey{Type: "ci", Backend: "backend-optout-cilist-down"}, &Cache{Entries: map[string]CacheEntry{
		"pr-1": {Content: json.RawMessage(`[{"id":"run-1","pr_id":"pr-1","as_of":"2026-01-01T00:00:00Z","stale":false}]`), AsOf: time.Now(), LastAccess: time.Now()},
	}})

	stdout, _, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stdout=%s", code, stdout)
	}
	var outcome ciListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Runs) != 0 {
		t.Fatalf("runs = %+v, want none: no cached content for an opted-out backend", outcome.Runs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceDegraded || outcome.Sources[0].Reason == cacheFallbackReason {
		t.Fatalf("sources = %+v, want one degraded row that is not the cache-fallback row", outcome.Sources)
	}
}

// assertAnnotationsUnchanged checks that raw still carries exactly the
// annotation set optOutAnnotations names.
func assertAnnotationsUnchanged(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var got, want map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode entity %s: %v", raw, err)
	}
	if err := json.Unmarshal([]byte("{"+optOutAnnotations+"}"), &want); err != nil {
		t.Fatalf("decode annotations: %v", err)
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Fatalf("annotation %q = %v, want %v (entity %s)", k, got[k], v, raw)
		}
	}
}

// targetedOptOutAnswers are the id-keyed read ops that pass through
// DispatchTargeted, each with an answer carrying the annotations.
func TestOptOutTargeted_FilesCommitsAndReviewPendingPassThroughAndWriteNoCache(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		op     string
		result string
	}{
		{"files", []string{"pr", "files", "pr-1"}, "files", `{"id":"pr-1","files":[{"path":"a.go","additions":5,"deletions":1}],` + optOutAnnotations + `}`},
		{"commits", []string{"pr", "commits", "pr-1"}, "commits", `{"id":"pr-1","commits":[{"sha":"abc","author":"a","message":"m"}],` + optOutAnnotations + `}`},
		{"review pending", []string{"pr", "review", "pending", "pr-1"}, "review_pending", `{"pending":false,"head_sha":"h","as_of":"2026-01-01T00:00:00Z",` + optOutAnnotations + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := "backend-optout-" + strings.ReplaceAll(tc.name, " ", "-")
			writeOpAwareFakeBackend(t, backend, map[string]string{
				tc.op: `{"protocolVersion":1,"schemaVersion":4,"result":` + tc.result + `}`,
			}, capsCacheOptOutPR)
			writeConfigFor(t, backend)

			stdout, _, code := executePrWithStdin(t, ``, tc.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
			}
			if got, want := decodeJSONValue(t, resp.Result), decodeJSONValue(t, []byte(tc.result)); !reflect.DeepEqual(got, want) {
				t.Fatalf("result = %s, want the backend's answer unchanged: %s", resp.Result, tc.result)
			}
			requireNoCacheFiles(t, backend)
		})
	}
}

// changesRemovedFor runs "pr changes" for a consumer already caught up to
// version 1 against a backend whose list answers empty, so the one ledger
// member o/r#1 is reported removed. The backend's own entity cache holds a
// detail copy of it.
func changesRemovedFor(t *testing.T, backend, caps string) (changesEntry, []byte, []byte) {
	t.Helper()
	writeOpAwareFakeBackend(t, backend, map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, caps)
	writeConfigFor(t, backend)
	seedLedger(t, LedgerKey{Type: "pr", Backend: backend, Query: "mine"}, &Ledger{
		Entries:   map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 1}},
		Version:   1,
		Consumers: map[string]ConsumerState{"c1": {Cursor: 1, LastSeen: time.Now()}},
	})
	key := CacheKey{Type: "pr", Backend: backend}
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: json.RawMessage(`{"id":"o/r#1","title":"cached before removal"}`), AsOf: time.Now(), LastAccess: time.Now()},
	}})
	path, err := cachePath(key)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded cache: %v", err)
	}

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 || string(w.Changes[0].Change) != "removed" {
		t.Fatalf("Changes = %+v, want exactly one removed entry", w.Changes)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache after changes: %v", err)
	}
	return w.Changes[0], before, after
}

func TestOptOutChanges_LedgerPathMakesNoTombstoneAndNoCacheWrite(t *testing.T) {
	entry, before, after := changesRemovedFor(t, "backend-optout-changes", capsCacheOptOutPR)
	if string(before) != string(after) {
		t.Fatalf("cache file changed for an opted-out backend:\nbefore: %s\nafter: %s", before, after)
	}
	if strings.Contains(string(entry.Entity), "cached before removal") {
		t.Fatalf("removed entity = %s, want the ledger's own envelope, not content read from the umbrella cache", entry.Entity)
	}
}

func TestOptOutChanges_BackendDeclaringNeitherStillTombstonesAsToday(t *testing.T) {
	entry, _, after := changesRemovedFor(t, "backend-nooptout-changes", capsPlainPR)
	if !strings.Contains(string(entry.Entity), "cached before removal") {
		t.Fatalf("removed entity = %s, want the cached content", entry.Entity)
	}
	var c Cache
	if err := json.Unmarshal(after, &c); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if e, ok := c.Entries["o/r#1"]; ok && e.RemovedAt == nil {
		t.Fatalf("cache entry = %+v, want tombstoned or evicted after the removed report", e)
	}
}
