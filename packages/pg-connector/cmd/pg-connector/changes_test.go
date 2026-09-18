package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeMarkerBackend creates an executable shell script named name that,
// if ever invoked, appends a line to markerPath and answers with a wire
// error envelope — used to PROVE a backend was never called (e.g.
// --cached) by asserting markerPath does not exist afterward.
func writeMarkerBackend(t *testing.T, name, markerPath string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	content := "#!/bin/sh\ncat >/dev/null\necho invoked >>" + markerPath + "\nprintf '{\"protocolVersion\":1,\"schemaVersion\":1,\"error\":{\"code\":\"unavailable\",\"message\":\"must not be called\"}}\\n'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write marker backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// seedLedger saves l for key directly, bypassing the CLI, so a test can
// establish precise pre-call ledger state (an existing entry, a
// tombstone, a stale consumer, an already-advanced cursor) without first
// driving a real backend call to build it up.
func seedLedger(t *testing.T, key LedgerKey, l *Ledger) {
	t.Helper()
	if err := saveLedger(key, l); err != nil {
		t.Fatalf("seed ledger %+v: %v", key, err)
	}
}

func decodeChangesWire(t *testing.T, stdout string) changesWire {
	t.Helper()
	var w changesWire
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("decode changes wire: %v (stdout=%s)", err, stdout)
	}
	return w
}

func changeKindsFor(entries []changesEntry) map[string]int {
	out := map[string]int{}
	for _, e := range entries {
		out[string(e.Change)]++
	}
	return out
}

// TestRun_PrChanges_FreshLedger_ReportsAddedThenEmptyOnNoUpstreamChange
// covers this packet's first two acceptance criteria together: a fresh
// ledger's first call reports every matching entity as added and
// advances the cursor; a second call with no upstream change reports an
// empty changes array.
func TestRun_PrChanges_FreshLedger_ReportsAddedThenEmptyOnNoUpstreamChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-fresh", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false},{"id":"o/r#2","repo":"o/r","number":2,"title":"t2","state":"open","branch":"b2","base":"main","author":"a","url":"u2","draft":false,"merged":false}],"present_ids":["o/r#1","o/r#2"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-changes-fresh")

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("first call: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 2 {
		t.Fatalf("first call: len(Changes) = %d, want 2: %+v", len(w.Changes), w.Changes)
	}
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 {
		t.Fatalf("first call: kinds = %+v, want 2 added", kinds)
	}
	for _, c := range w.Changes {
		if c.Source != "backend-changes-fresh" {
			t.Fatalf("change %+v: Source = %q, want the backend name", c, c.Source)
		}
	}
	if len(w.Sources) != 1 || w.Sources[0].Status != SourceSucceeded {
		t.Fatalf("first call: Sources = %+v", w.Sources)
	}

	stdout2, _, code2 := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code2 != 0 {
		t.Fatalf("second call: exit code = %d, want 0; stdout=%s", code2, stdout2)
	}
	w2 := decodeChangesWire(t, stdout2)
	if len(w2.Changes) != 0 {
		t.Fatalf("second call (no upstream change): Changes = %+v, want empty", w2.Changes)
	}
}

// TestRun_PrChanges_Cached_NeverCallsBackend covers "--cached never calls
// the backend ... and still returns whatever the ledger already knows
// past the cursor" — proven both by a marker backend that would fail this
// test if invoked, and by asserting the reported entity comes from
// pre-seeded ledger state alone.
func TestRun_PrChanges_Cached_NeverCallsBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "invoked.log")
	writeMarkerBackend(t, "backend-changes-cached", marker)
	writeConfigFor(t, "backend-changes-cached")

	key := LedgerKey{Type: "pr", Backend: "backend-changes-cached", Query: "mine"}
	seedLedger(t, key, &Ledger{
		Entries:   map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 1}},
		Version:   1,
		Consumers: map[string]ConsumerState{},
	})

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1", "--cached"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("--cached invoked the backend (marker file exists): %s", marker)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 || string(w.Changes[0].Change) != "added" {
		t.Fatalf("Changes = %+v, want exactly one 'added' entry for o/r#1 (a cursor-0 consumer's first cached read)", w.Changes)
	}

	// The consumer's cursor still advances on a cached call (design:
	// section 5.3's advance-after-flush rule is not itself --cached-
	// conditional — only Evict is excluded for --cached).
	loaded, err := loadLedger(key)
	if err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	if cs := loaded.Consumers["c1"]; cs.Cursor != 1 {
		t.Fatalf("consumer c1 cursor after cached call = %d, want 1 (advanced to the ledger's current version)", cs.Cursor)
	}
}

// TestRun_PrChanges_Reset_ReplaysLiveAsAddedWithNoRemovedTombstone covers
// "--reset reports every live entity as added again on the next call,
// with no removed tombstone for anything that was actually removed
// before the reset."
func TestRun_PrChanges_Reset_ReplaysLiveAsAddedWithNoRemovedTombstone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	marker := filepath.Join(t.TempDir(), "invoked.log")
	writeMarkerBackend(t, "backend-changes-reset", marker)
	writeConfigFor(t, "backend-changes-reset")

	removedAt := int64(2)
	key := LedgerKey{Type: "pr", Backend: "backend-changes-reset", Query: "mine"}
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{
			"o/r#1": {Hash: "h1", VersionLastChanged: 1},
			"o/r#2": {Hash: "h2", VersionLastChanged: 2, RemovedAtVersion: &removedAt},
		},
		Version:   2,
		Consumers: map[string]ConsumerState{"c1": {Cursor: 2, LastSeen: time.Now()}}, // already fully caught up
	})

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1", "--reset", "--cached"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 {
		t.Fatalf("Changes = %+v, want exactly 1 entry (the live o/r#1, replayed as added; the removed o/r#2 must not appear)", w.Changes)
	}
	c := w.Changes[0]
	id, err := entityID(c.Entity)
	if err != nil || id != "o/r#1" {
		t.Fatalf("Changes[0] = %+v, want o/r#1", c)
	}
	if string(c.Change) != "added" {
		t.Fatalf("Changes[0].Change = %q, want %q (a just-reset consumer has never seen it before)", c.Change, "added")
	}
}

// TestFanOutChanges_CrashBetweenFlushAndAdvance_ReproducesDuplicateDelivery
// mechanically proves the "duplicate, never loss" guarantee: a call whose
// own commitAdvances step is skipped (simulating a crash after output was
// written and flushed but before the cursor advance was persisted) causes
// the NEXT real call to report the same entries again.
func TestFanOutChanges_CrashBetweenFlushAndAdvance_ReproducesDuplicateDelivery(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-crash", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - backend-changes-crash\n"), "test")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if err := ensureLedgerDirExists(); err != nil {
		t.Fatalf("ensureLedgerDirExists: %v", err)
	}
	ctx := context.Background()

	// "Crashed" call: refresh+save already happened inside fanOutChanges
	// (this is what leaves the entries/version on disk), output was
	// (hypothetically) written and flushed, but commitAdvances is
	// deliberately never called — simulating the crash window design
	// section 5.3 describes.
	crashed := fanOutChanges(ctx, reg, "pr", []string{"backend-changes-crash"}, "mine", "c1", false, false)
	if len(crashed) != 1 || len(crashed[0].entries) != 1 || string(crashed[0].entries[0].Change) != "added" {
		t.Fatalf("crashed call results = %+v, want exactly one added entry", crashed)
	}

	// The real next call: since the crash never advanced/persisted the
	// consumer's cursor, this must reproduce the exact same "added"
	// entry again (a duplicate delivery, never a loss).
	real := fanOutChanges(ctx, reg, "pr", []string{"backend-changes-crash"}, "mine", "c1", false, false)
	if len(real) != 1 || len(real[0].entries) != 1 {
		t.Fatalf("real call results = %+v, want exactly one entry (the duplicate)", real)
	}
	if id, _ := entityID(real[0].entries[0].Entity); id != "o/r#1" {
		t.Fatalf("real call reported %+v, want a duplicate of o/r#1", real[0].entries[0])
	}
	if string(real[0].entries[0].Change) != "added" {
		t.Fatalf("real call Change = %q, want %q (same duplicate classification as the crashed call)", real[0].entries[0].Change, "added")
	}

	// This call completes normally — commit its advance so a THIRD call
	// (not exercised further here) would not see the duplicate again.
	if err := commitAdvances(real, "c1"); err != nil {
		t.Fatalf("commitAdvances: %v", err)
	}
}

// TestRun_PrChanges_FanOutExitCode_DegradedAndTotalFailure covers "changes
// 's own fan-out exit code matches list's existing scheme (0/2/3) ... on a
// synthetic degraded/total-failure backend set."
func TestRun_PrChanges_FanOutExitCode_DegradedAndTotalFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-ok", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeOpAwareFakeBackend(t, "backend-changes-bad", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"down"}}`,
	}, `{}`)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-changes-ok\n    - backend-changes-bad\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (degraded: one healthy, one failed); stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Sources) != 2 {
		t.Fatalf("Sources = %+v, want one row per backend", w.Sources)
	}
}

// TestRun_PrChanges_DegradedReason_SurfacesInJSONAndHuman covers this
// bead's own acceptance criterion (pg2-unqcn): fanOutChanges/
// classifyListSource already capture a degraded/failed backend's real
// cause (e.g. a rate-limit guard tripping) internally, but until
// changesSourceRow/humanizeChangesOutcome carried a Reason field it was
// silently dropped, leaving only "degraded" with no cause. Confirms the
// real reason now reaches both --output json's sources[].reason and
// --output human's rendered sources: line, mirroring pr.go's existing
// SourceResult{Reason}/formatSourcesTable "(%s)" handling.
func TestRun_PrChanges_DegradedReason_SurfacesInJSONAndHuman(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	const reason = "scriptout: unavailable: backend-changes-reason-bad: GraphQL rate limit remaining (759) is below the configured reserve (1000)"
	// Two backends (one healthy, one failing) so the overall call is
	// "degraded" (exit 2) rather than "total failure" (exit 3), matching
	// TestRun_PrChanges_FanOutExitCode_DegradedAndTotalFailure's own
	// pattern — this test's own focus is the Reason content, not the
	// exit-code scheme (already covered by that other test).
	writeOpAwareFakeBackend(t, "backend-changes-reason-ok", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeOpAwareFakeBackend(t, "backend-changes-reason-bad", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"` + reason + `"}}`,
	}, `{}`)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-changes-reason-ok\n    - backend-changes-reason-bad\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (degraded); stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Sources) != 2 {
		t.Fatalf("Sources = %+v, want one row per backend", w.Sources)
	}
	var badReason string
	for _, s := range w.Sources {
		if s.Backend == "backend-changes-reason-bad" {
			badReason = s.Reason
		}
	}
	if !strings.Contains(badReason, "rate limit") {
		t.Fatalf("bad backend's Reason = %q, want it to contain the real backend error, not be swallowed", badReason)
	}

	stdoutHuman, _, codeHuman := executePr(t, []string{"--output", "human", "pr", "changes", "--query", "mine", "--consumer", "c1"})
	if codeHuman != 2 {
		t.Fatalf("human exit code = %d, want 2; stdout=%s", codeHuman, stdoutHuman)
	}
	if !strings.Contains(stdoutHuman, "rate limit") {
		t.Fatalf("human output missing the real reason; stdout=%s", stdoutHuman)
	}
}

// TestRun_PrChanges_QueryNotRecognized_ExcludedFromDegradedAccounting
// covers the "query_not_recognized excluded from degraded accounting"
// half of the same acceptance criterion, reusing list.go's own
// classifyListSource/allQueryNotRecognized (not reimplemented here): one
// backend answers normally, the other answers query_not_recognized — the
// overall call must still succeed (exit 0), not be reported degraded.
func TestRun_PrChanges_QueryNotRecognized_ExcludedFromDegradedAccounting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-qnr-ok", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeOpAwareFakeBackend(t, "backend-changes-qnr-bad", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"query_not_recognized","message":"nope"}}`,
	}, `{}`)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-changes-qnr-ok\n    - backend-changes-qnr-bad\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (query_not_recognized excluded from degraded accounting); stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	var sawDisabled bool
	for _, s := range w.Sources {
		if s.Backend == "backend-changes-qnr-bad" {
			if s.Status != SourceDisabled {
				t.Fatalf("qnr backend status = %q, want disabled", s.Status)
			}
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatalf("Sources = %+v, missing the query_not_recognized backend's row", w.Sources)
	}

	// Rule 3 also wipes/deletes the ledger file for that backend's key —
	// confirm it's gone.
	keys, err := ListLedgerKeys()
	if err != nil {
		t.Fatalf("ListLedgerKeys: %v", err)
	}
	for _, k := range keys {
		if k.Backend == "backend-changes-qnr-bad" {
			t.Fatalf("ledger key %+v still exists after a query_not_recognized refresh (rule 3 should have deleted it)", k)
		}
	}
}

// TestRun_PrChanges_EvictRunsAfterNonCachedRefresh_NotAfterCached covers
// "a non-cached changes call runs Evict after its Refresh (a consumer
// cursor already older than consumer_prune_after observes it pruned
// after one such call); a --cached call does NOT run Evict (the same
// stale cursor survives a --cached-only call)."
func TestRun_PrChanges_EvictRunsAfterNonCachedRefresh_NotAfterCached(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-evict", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	cfgBody := "connector:\n  pr:\n    - backend-changes-evict\nstate:\n  consumer_prune_after: \"1d\"\n"
	if err := os.WriteFile(cfg, []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	key := LedgerKey{Type: "pr", Backend: "backend-changes-evict", Query: "mine"}
	staleConsumer := func() map[string]ConsumerState {
		return map[string]ConsumerState{"stale": {Cursor: 0, LastSeen: time.Now().Add(-2 * 24 * time.Hour)}}
	}

	// --cached: Evict must NOT run, so the stale consumer survives.
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Version: 0, Consumers: staleConsumer()})
	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "other", "--cached"})
	if code != 0 {
		t.Fatalf("cached call: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	afterCached, err := loadLedger(key)
	if err != nil {
		t.Fatalf("reload after cached call: %v", err)
	}
	if _, ok := afterCached.Consumers["stale"]; !ok {
		t.Fatalf("--cached call pruned the stale consumer; Evict must not run on a cached read")
	}

	// Non-cached: Evict must run and prune the stale consumer.
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Version: 0, Consumers: staleConsumer()})
	stdout2, _, code2 := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "other"})
	if code2 != 0 {
		t.Fatalf("non-cached call: exit code = %d, want 0; stdout=%s", code2, stdout2)
	}
	afterRefresh, err := loadLedger(key)
	if err != nil {
		t.Fatalf("reload after non-cached call: %v", err)
	}
	if _, ok := afterRefresh.Consumers["stale"]; ok {
		t.Fatalf("non-cached call left the stale consumer in place; Evict should have pruned it (rule 2)")
	}
}

// TestRun_PrChanges_HumanOutput smoke-checks --output human never leaks
// raw JSON, mirroring pr.go's own TestRun_PrFiles_HumanOutput.
func TestRun_PrChanges_HumanOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-human", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-changes-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "added") || !strings.Contains(stdout, "o/r#1") {
		t.Fatalf("human output missing expected content; stdout=%s", stdout)
	}
}

// TestRun_IssueChanges_Wired proves "changes" is wired for issue too (not
// just pr), matching this packet's own Contract/Produces list.
func TestRun_IssueChanges_Wired(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-issue-changes", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"ISSUE-1","title":"t","state":"open"}],"present_ids":["ISSUE-1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	dirCfg := t.TempDir()
	cfg := filepath.Join(dirCfg, "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  issue:\n    - backend-issue-changes\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"issue", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 {
		t.Fatalf("Changes = %+v, want 1", w.Changes)
	}
}

// TestChangesRemovedCarriesLastCachedContent covers this packet's own
// Validation item (d): a "pr changes" call whose backend reports an id as
// removed, when that id has a live entry in this docket's own umbrella
// entity cache, reports the CACHED content instead of the ordinary
// id-only envelope — and, as a regression check on this file's own new
// step 6, that reported removal then tombstones the cache's own copy too
// (Cache.Remove), so a later show/list cache-fallback stops offering it.
func TestChangesRemovedCarriesLastCachedContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-removed", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-changes-removed")

	// A consumer already caught up to version 1 -- so THIS call's own
	// removal (version 2, computed below by Refresh) is new to it and
	// preCursor != 0, the precondition mergeChanges' own ChangeRemoved
	// branch requires before reporting anything at all.
	key := LedgerKey{Type: "pr", Backend: "backend-changes-removed", Query: "mine"}
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{
			"o/r#1": {Hash: "h1", VersionLastChanged: 1},
		},
		Version:   1,
		Consumers: map[string]ConsumerState{"c1": {Cursor: 1, LastSeen: time.Now()}},
	})
	seedCache(t, CacheKey{Type: "pr", Backend: "backend-changes-removed"}, &Cache{
		Entries: map[string]CacheEntry{
			"o/r#1": {
				Content:    json.RawMessage(`{"id":"o/r#1","title":"cached before removal"}`),
				AsOf:       time.Now(),
				LastAccess: time.Now(),
			},
		},
	})

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 {
		t.Fatalf("Changes = %+v, want exactly 1 removed entry", w.Changes)
	}
	c := w.Changes[0]
	if string(c.Change) != "removed" {
		t.Fatalf("Changes[0].Change = %q, want removed", c.Change)
	}
	if !strings.Contains(string(c.Entity), "cached before removal") {
		t.Fatalf("Changes[0].Entity = %s, want the CACHED content, not id-only", c.Entity)
	}

	// Regression on this file's own new step 6: the cache's own copy is no
	// longer served as a live hit -- Cache.Remove tombstones it, and since
	// consumer c1 (the only known consumer) is ALSO the one this very
	// call just advanced past the removal, Cache.Evict's own
	// consumersPassed check (built from this same call's already-advanced
	// ledger) may legitimately drop the tombstoned entry entirely in the
	// same call, exactly mirroring Ledger.Evict's own identical rule 1
	// ("vacuously true when zero consumers remain" -- here, "true once no
	// consumer still needs it"). Either outcome (absent, or present but
	// tombstoned) is correct; what must NOT happen is the entry surviving
	// as still-live.
	c2, err := loadCache(CacheKey{Type: "pr", Backend: "backend-changes-removed"})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if entry, ok := c2.Entries["o/r#1"]; ok && entry.RemovedAt == nil {
		t.Fatalf("cache entry for o/r#1 = %+v, want tombstoned or evicted after the removed report (not still live)", entry)
	}
}

// TestChangesRemovedFallsBackToIDOnlyWhenNoCacheEntry is the regression
// half of Validation item (d): an id with no matching cache entry
// (opted out, expired, or already evicted -- here, simply never cached)
// still carries id-only, exactly as before this packet.
func TestChangesRemovedFallsBackToIDOnlyWhenNoCacheEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeOpAwareFakeBackend(t, "backend-changes-removed-nocache", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-changes-removed-nocache")

	key := LedgerKey{Type: "pr", Backend: "backend-changes-removed-nocache", Query: "mine"}
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{
			"o/r#1": {Hash: "h1", VersionLastChanged: 1},
		},
		Version:   1,
		Consumers: map[string]ConsumerState{"c1": {Cursor: 1, LastSeen: time.Now()}},
	})

	stdout, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if len(w.Changes) != 1 {
		t.Fatalf("Changes = %+v, want exactly 1 removed entry", w.Changes)
	}
	c := w.Changes[0]
	if string(c.Change) != "removed" {
		t.Fatalf("Changes[0].Change = %q, want removed", c.Change)
	}
	var idOnly struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(c.Entity, &idOnly); err != nil {
		t.Fatalf("decode Changes[0].Entity: %v", err)
	}
	if idOnly.ID != "o/r#1" {
		t.Fatalf("Changes[0].Entity id = %q, want o/r#1", idOnly.ID)
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(c.Entity, &extra); err != nil {
		t.Fatalf("decode Changes[0].Entity as map: %v", err)
	}
	if len(extra) != 1 {
		t.Fatalf("Changes[0].Entity = %s, want the id-only envelope unchanged (exactly one key)", c.Entity)
	}
}
