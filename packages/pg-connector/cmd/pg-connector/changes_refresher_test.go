package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// The changes refresher (INV-CACHE-6, INV-CACHE-7): membership plus a fetch of
// only the new or aged members, removal confirmation, post-flush detail
// writes, and the freshness stamp of a partial pass.

// refresherEnv sets up a scripted pr backend and a registry file with the
// given state: lines, isolating the ledger and cache under a temp dir.
func refresherEnv(t *testing.T, name, state string) *scriptedBackend {
	t.Helper()
	b := newScriptedBackend(t, name)
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	yaml := "connector:\n  pr:\n    - " + name + "\n"
	if state != "" {
		yaml += "state:\n" + state
	}
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	return b
}

func runChanges(t *testing.T, args ...string) changesWire {
	t.Helper()
	full := append([]string{"pr", "changes", "--query", "mine", "--consumer", "c1"}, args...)
	stdout, stderr, code := executePr(t, full)
	if code != 0 {
		t.Fatalf("changes exit = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	return decodeChangesWire(t, stdout)
}

func loadTestCache(t *testing.T, backend string) *Cache {
	t.Helper()
	c, err := loadCache(CacheKey{Type: "pr", Backend: backend})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	return c
}

func TestChangesRefresher_FetchesOnlyNewOrAgedMembers(t *testing.T) {
	b := refresherEnv(t, "backend-rf-aged", "  cache_refresh_after: 2m\n")
	now := time.Now()
	for _, id := range []string{"pr-1", "pr-2"} {
		b.setEntity(t, id, "t1", now)
	}
	b.setMembership(t, "pr-1", "pr-2")

	// First pass: both are new.
	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 {
		t.Fatalf("first pass kinds = %v, want 2 added: %+v", kinds, w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("first pass shows = %d, want 2", n)
	}
	c := loadTestCache(t, b.name)
	for _, id := range []string{"pr-1", "pr-2"} {
		if e := c.Entries[id]; e.level() != CacheLevelDetail {
			t.Fatalf("cache entry %s level = %q, want detail (written after the flush)", id, e.Level)
		}
	}

	// Second pass: both are young, so no show, no change, and the id-only
	// membership is the only origin call.
	b.setEntity(t, "pr-2", "CHANGED", now) // changes upstream, but its entry is young
	w = runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("second pass changes = %+v, want none (members are within refresh_after)", w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("second pass shows = %d total, want still 2", n)
	}

	// Age pr-2's entry past refresh_after: it is re-fetched and reported changed.
	c = loadTestCache(t, b.name)
	e := c.Entries["pr-2"]
	e.AsOf = now.Add(-10 * time.Minute)
	c.Entries["pr-2"] = e
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, c)
	w = runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["changed"] != 1 || len(w.Changes) != 1 {
		t.Fatalf("third pass = %+v, want exactly one changed", w.Changes)
	}
	if n := b.calls(t, "show"); n != 3 {
		t.Fatalf("third pass shows = %d total, want 3 (only the aged one)", n)
	}
}

func TestChangesRefresher_OffByDefaultUsesTheFullList(t *testing.T) {
	b := refresherEnv(t, "backend-rf-off", "")
	b.set(t, "list-full.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"pr-1","title":"t","state":"open","as_of":%q,"stale":false}],"present_ids":["pr-1"],"cursor":null,"truncated":false}}`, time.Now().UTC().Format(time.RFC3339)))

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 1 {
		t.Fatalf("kinds = %v, want 1 added", kinds)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0 (refresher is opt-in)", n)
	}
	// The ordinary path writes the listed entity at summary level, post-flush.
	if e := loadTestCache(t, b.name).Entries["pr-1"]; e.level() != CacheLevelSummary || len(e.Content) == 0 {
		t.Fatalf("cache entry = %+v, want a summary-level entry", e)
	}
}

func TestChangesRefresher_RemovalIsConfirmedByAReadAndCarriesTheConfirmedContent(t *testing.T) {
	b := refresherEnv(t, "backend-rf-remove", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setEntity(t, "pr-2", "open", now)
	b.setMembership(t, "pr-1", "pr-2")
	runChanges(t)

	// pr-2 leaves the query and now reads as merged.
	b.setMembership(t, "pr-1")
	b.set(t, "show-pr-2.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-2","title":"open","state":"merged","as_of":%q,"stale":false}}`, now.UTC().Format(time.RFC3339)))
	w := runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("changes = %+v, want one removed", w.Changes)
	}
	var e struct{ State string }
	if err := json.Unmarshal(w.Changes[0].Entity, &e); err != nil || e.State != "merged" {
		t.Fatalf("removed entity = %s, want the confirmed merged content", w.Changes[0].Entity)
	}
}

func TestChangesRefresher_NotFoundConfirmsTheRemovalWithLastContent(t *testing.T) {
	b := refresherEnv(t, "backend-rf-gone", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setMembership(t, "pr-1")
	runChanges(t)

	b.setMembership(t)
	if err := os.Remove(b.dir + "/show-pr-1.json"); err != nil {
		t.Fatalf("remove entity: %v", err)
	}
	w := runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("changes = %+v, want one removed", w.Changes)
	}
	var e struct{ Title string }
	if err := json.Unmarshal(w.Changes[0].Entity, &e); err != nil || e.Title != "open" {
		t.Fatalf("removed entity = %s, want the last cached content", w.Changes[0].Entity)
	}
}

func TestChangesRefresher_AFailedConfirmationWithholdsTheRemoval(t *testing.T) {
	b := refresherEnv(t, "backend-rf-withhold", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setMembership(t, "pr-1")
	runChanges(t)

	b.setMembership(t)
	b.set(t, "show-error", "unavailable")
	w := runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("changes = %+v, want the removal withheld while the confirmation read fails", w.Changes)
	}
	if !w.Sources[0].Truncated {
		t.Fatalf("source row = %+v, want truncated (the pass was incomplete)", w.Sources[0])
	}
	// The failed pass must not stamp refreshed_at (INV-CACHE-6); the earlier
	// whole-query pass did.
	key := LedgerKey{Type: "pr", Backend: b.name, Query: "mine"}
	l, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want code truncated", l.LastError)
	}
	if l.Entries["pr-1"].RemovedAtVersion != nil {
		t.Fatal("the withheld removal was tombstoned in the ledger")
	}

	// Once the confirmation can be made, the next call reports it.
	if err := os.Remove(b.dir + "/show-error"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b.dir + "/show-pr-1.json"); err != nil {
		t.Fatal(err)
	}
	w = runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("retry changes = %+v, want one removed", w.Changes)
	}
}

func TestChangesRefresher_APartialPassRecordsNoRefreshedAt(t *testing.T) {
	b := refresherEnv(t, "backend-rf-partial", "  cache_refresh_after: 2m\n")
	b.setMembership(t, "pr-1")
	b.set(t, "show-error", "unavailable") // the new member cannot be fetched

	w := runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", w.Changes)
	}
	l, err := loadLedger(LedgerKey{Type: "pr", Backend: b.name, Query: "mine"})
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if l.RefreshedAt != nil {
		t.Fatalf("RefreshedAt = %v, want nil for a pass that failed to fetch a member", l.RefreshedAt)
	}
	if l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want truncated", l.LastError)
	}
}

func TestChangesRefresher_AWholePassStampsRefreshedAtAndCachedReadDoesNot(t *testing.T) {
	b := refresherEnv(t, "backend-rf-stamp", "  cache_refresh_after: 2m\n")
	b.setEntity(t, "pr-1", "t", time.Now())
	b.setMembership(t, "pr-1")
	runChanges(t)
	key := LedgerKey{Type: "pr", Backend: b.name, Query: "mine"}
	l, _ := loadLedger(key)
	if l.RefreshedAt == nil || l.LastError != nil {
		t.Fatalf("after a whole pass RefreshedAt=%v LastError=%+v, want stamped and no error", l.RefreshedAt, l.LastError)
	}
	first := *l.RefreshedAt

	before := b.calls(t, "list")
	runChanges(t, "--cached")
	if b.calls(t, "list") != before {
		t.Fatal("--cached called the backend")
	}
	l, _ = loadLedger(key)
	if !l.RefreshedAt.Equal(first) {
		t.Fatalf("a --cached read moved RefreshedAt from %v to %v", first, *l.RefreshedAt)
	}
}

func TestChangesRefresher_TypeOptOutKeepsTheOrdinaryPath(t *testing.T) {
	b := refresherEnv(t, "backend-rf-optout", "  cache_refresh_after: 2m\n  cache_disabled_types: pr\n")
	b.set(t, "list-full.json", `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"pr-1","title":"t","state":"open"}],"present_ids":["pr-1"],"cursor":null,"truncated":false}}`)
	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 1 {
		t.Fatalf("kinds = %v, want 1 added", kinds)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0 (an opted-out type never runs the refresher)", n)
	}
}

func TestRefresherEnabled_RequiresPositiveRefreshAfterAndAPrOrIssueType(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	newScriptedBackend(t, "backend-rf-enabled")
	ctx := context.Background()
	cases := []struct {
		state string
		typ   string
		want  bool
	}{
		{"", "pr", false},
		{"  cache_refresh_after: 2m\n", "pr", true},
		{"  cache_refresh_after: 2m\n", "issue", true},
		{"  cache_refresh_after: 2m\n", "calendar", false},
		{"  cache_refresh_after: off\n", "pr", false},
		{"  cache_refresh_after: nonsense\n", "pr", false},
	}
	for _, tc := range cases {
		reg := registryFor(t, "backend-rf-enabled", tc.state)
		_, got := refresherEnabled(ctx, reg, tc.typ, "backend-rf-enabled")
		if got != tc.want {
			t.Errorf("refresherEnabled(state=%q, type=%s) = %t, want %t", tc.state, tc.typ, got, tc.want)
		}
	}
}
