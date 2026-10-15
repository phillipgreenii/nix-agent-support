package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// The batched fetch (bead pg2-12yur): a backend that registers show_many is
// asked for the members it needs in chunks, and a summary it answers counts as
// fresh for the refresher (the refresher-specific freshness stamp) without
// making the entity look detail-fresh.

// enableShowMany makes the scripted backend list show_many in its capabilities.
func (b *scriptedBackend) enableShowMany(t *testing.T) {
	t.Helper()
	b.set(t, "show_many-enabled", "")
}

// setMembershipNodes is setMembership with a node id ("N-<id>") for every id,
// as an ids-only pr list reports them.
func (b *scriptedBackend) setMembershipNodes(t *testing.T, ids ...string) {
	t.Helper()
	b.setMembershipNodesExcept(t, nil, ids...)
}

// setMembershipNodesExcept is setMembershipNodes leaving the ids in noNode
// without a node id.
func (b *scriptedBackend) setMembershipNodesExcept(t *testing.T, noNode map[string]bool, ids ...string) {
	t.Helper()
	quoted := make([]string, len(ids))
	nodes := []string{}
	for i, id := range ids {
		quoted[i] = strconv.Quote(id)
		if !noNode[id] {
			nodes = append(nodes, fmt.Sprintf("%q:%q", id, "N-"+id))
		}
	}
	b.set(t, "list-ids.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":13,"result":{"entities":[],"present_ids":[%s],"node_ids":{%s},"cursor":null,"truncated":false}}`,
		strings.Join(quoted, ","), strings.Join(nodes, ",")))
}

// summaryEntity is a minimal show_many summary entity (no body fields).
func summaryEntity(id, title string, asOf time.Time) string {
	return fmt.Sprintf(`{"id":%q,"title":%q,"state":"open","as_of":%q,"stale":false}`, id, title, asOf.UTC().Format(time.RFC3339))
}

// setShowMany scripts the answer to show_many call n (1-based; 0 answers every
// call without a numbered script).
func (b *scriptedBackend) setShowMany(t *testing.T, n int, entities []string, missing []string) {
	t.Helper()
	quoted := make([]string, len(missing))
	for i, id := range missing {
		quoted[i] = strconv.Quote(id)
	}
	name := "show_many.json"
	if n > 0 {
		name = fmt.Sprintf("show_many-%d.json", n)
	}
	b.set(t, name, fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":13,"result":{"entities":[%s],"missing":[%s]}}`,
		strings.Join(entities, ","), strings.Join(quoted, ",")))
}

// showManyRefs returns the refs each show_many call named, in call order.
func (b *scriptedBackend) showManyRefs(t *testing.T) [][]struct{ ID, NodeID string } {
	t.Helper()
	data, err := os.ReadFile(b.dir + "/show_many.requests")
	if err != nil {
		return nil
	}
	var out [][]struct{ ID, NodeID string }
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req struct {
			Args struct {
				Refs []struct {
					ID     string `json:"id"`
					NodeID string `json:"node_id"`
				} `json:"refs"`
			} `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("decode show_many request %q: %v", line, err)
		}
		refs := make([]struct{ ID, NodeID string }, len(req.Args.Refs))
		for i, r := range req.Args.Refs {
			refs[i] = struct{ ID, NodeID string }{r.ID, r.NodeID}
		}
		out = append(out, refs)
	}
	return out
}

func membershipIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("pr-%03d", i+1)
	}
	return ids
}

func summaryEntities(ids []string, asOf time.Time) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = summaryEntity(id, "t-"+id, asOf)
	}
	return out
}

func TestRefreshFetchChunkSize_Is74(t *testing.T) {
	if refreshFetchChunkSize != 74 {
		t.Fatalf("refreshFetchChunkSize = %d, want 74: the GitHub backend costs 1 GraphQL point for 1 to 74 ids and 2 for 75 to 100; re-measure before changing it", refreshFetchChunkSize)
	}
}

// One show_many call replaces one show per member: every new member is fetched
// by id, with its node id, as a summary.
func TestChangesRefresher_BatchedFetchReplacesPerMemberShows(t *testing.T) {
	b := refresherEnv(t, "backend-rf-batch", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	ids := []string{"pr-1", "pr-2", "pr-3"}
	b.setMembershipNodes(t, ids...)
	b.setShowMany(t, 0, summaryEntities(ids, now), nil)

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 3 || len(w.Changes) != 3 {
		t.Fatalf("changes = %+v, want 3 added", w.Changes)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0 (one batched call replaces them)", n)
	}
	refs := b.showManyRefs(t)
	if len(refs) != 1 || len(refs[0]) != 3 {
		t.Fatalf("show_many requests = %+v, want one call naming 3 refs", refs)
	}
	for _, r := range refs[0] {
		if r.NodeID != "N-"+r.ID {
			t.Errorf("ref %+v does not carry the node id the membership reported", r)
		}
	}
	// Summaries are written at summary level, with the refresher's stamp.
	c := loadTestCache(t, b.name)
	for _, id := range ids {
		e := c.Entries[id]
		if e.level() != CacheLevelSummary || e.RefreshedAt == nil {
			t.Fatalf("cache entry %s = %+v, want a summary entry carrying the refresher's stamp", id, e)
		}
	}
	l, _ := loadLedger(LedgerKey{Type: "pr", Backend: b.name, Query: "mine"})
	if l.RefreshedAt == nil || l.LastError != nil {
		t.Fatalf("a whole batched pass must stamp refreshed_at: RefreshedAt=%v LastError=%+v", l.RefreshedAt, l.LastError)
	}
}

// The freshness-stamp invariant: a summary write counts as fresh for the
// refresher, so a second pass within refresh_after fetches nothing, and it does
// NOT make the entity detail-fresh, so a show read-through still goes to the
// origin (INV-CACHE-2 is unchanged).
func TestChangesRefresher_ASummaryFetchIsFreshForTheRefresherButNotDetailFresh(t *testing.T) {
	b := refresherEnv(t, "backend-rf-stamp-inv", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	ids := []string{"pr-1", "pr-2"}
	b.setMembershipNodes(t, ids...)
	b.setShowMany(t, 0, summaryEntities(ids, now), nil)
	runChanges(t)

	// Within refresh_after nothing is fetched: no show_many, no show.
	w := runChanges(t)
	if len(w.Changes) != 0 || len(b.showManyRefs(t)) != 1 || b.calls(t, "show") != 0 {
		t.Fatalf("second pass: changes=%+v show_many calls=%d shows=%d, want an idle pass", w.Changes, len(b.showManyRefs(t)), b.calls(t, "show"))
	}

	c := loadTestCache(t, b.name)
	at := time.Now()
	for _, id := range ids {
		if !c.RefreshYoung(id, 2*time.Minute, at) {
			t.Errorf("RefreshYoung(%s) = false after a summary refresh, want true", id)
		}
		if _, _, ok := c.GetDetail(id, time.Hour, at); ok {
			t.Errorf("GetDetail(%s) hit after a summary refresh: a summary write must not make an entity detail-fresh", id)
		}
	}
	// A show of it still reaches the backend (no read-through from a summary).
	b.setEntity(t, "pr-1", "detail", time.Now())
	if _, _, code := executePr(t, []string{"pr", "show", "pr-1"}); code != 0 {
		t.Fatal("show failed")
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("shows = %d, want 1: a summary entry never answers a show", n)
	}
}

// An aged member is re-fetched once, and the fetch re-arms the stamp: the next
// pass is idle again, so the refresher does not refetch every tick.
func TestChangesRefresher_AnAgedMemberIsRefetchedOnceThenIdleAgain(t *testing.T) {
	b := refresherEnv(t, "backend-rf-aged-batch", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	b.setMembershipNodes(t, "pr-1", "pr-2")
	b.setShowMany(t, 0, summaryEntities([]string{"pr-1", "pr-2"}, now), nil)
	runChanges(t)

	// Age pr-2's stamp past refresh_after.
	c := loadTestCache(t, b.name)
	old := now.Add(-10 * time.Minute)
	e := c.Entries["pr-2"]
	e.AsOf, e.RefreshedAt = old, &old
	c.Entries["pr-2"] = e
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, c)

	b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t-pr-1", now), summaryEntity("pr-2", "CHANGED", time.Now())}, nil)
	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["changed"] != 1 || len(w.Changes) != 1 {
		t.Fatalf("aged pass = %+v, want exactly pr-2 changed", w.Changes)
	}
	refs := b.showManyRefs(t)
	if len(refs) != 2 || len(refs[1]) != 1 || refs[1][0].ID != "pr-2" {
		t.Fatalf("show_many requests = %+v, want the second call to name only the aged pr-2", refs)
	}

	w = runChanges(t)
	if len(w.Changes) != 0 || len(b.showManyRefs(t)) != 2 {
		t.Fatalf("pass after the refetch: changes=%+v show_many calls=%d, want idle (the stamp was re-armed)", w.Changes, len(b.showManyRefs(t)))
	}
}

// A live detail entry keeps its content and its own as_of through a summary
// refresh (INV-CACHE-2), and the refresher's stamp is what settles the member.
func TestChangesRefresher_ASummaryRefreshLeavesADetailEntryIntact(t *testing.T) {
	b := refresherEnv(t, "backend-rf-detail-intact", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	b.setMembershipNodes(t, "pr-1")
	b.setShowMany(t, 0, summaryEntities([]string{"pr-1"}, now), nil)
	runChanges(t)

	oldDetail := now.Add(-time.Hour)
	c := loadTestCache(t, b.name)
	c.Entries["pr-1"] = CacheEntry{Content: json.RawMessage(`{"id":"pr-1","title":"DETAIL","as_of":"x"}`), AsOf: oldDetail, LastAccess: now, Level: CacheLevelDetail}
	seedCache(t, CacheKey{Type: "pr", Backend: b.name}, c)

	b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t-pr-1", time.Now())}, nil)
	runChanges(t)
	e := loadTestCache(t, b.name).Entries["pr-1"]
	if e.level() != CacheLevelDetail || !strings.Contains(string(e.Content), "DETAIL") || !e.AsOf.Equal(oldDetail) {
		t.Fatalf("entry = %+v, want the old detail content and as_of untouched", e)
	}
	if e.RefreshedAt == nil || time.Since(*e.RefreshedAt) > time.Minute {
		t.Fatalf("RefreshedAt = %v, want a fresh stamp", e.RefreshedAt)
	}
	before := len(b.showManyRefs(t))
	runChanges(t)
	if len(b.showManyRefs(t)) != before {
		t.Fatal("the stamp did not settle the member: it was fetched again")
	}
}

// Members are fetched in chunks of at most 74 ids, so a chunk's cost stays at
// one GraphQL point and one failure fails at most 74 ids.
func TestChangesRefresher_FetchesInChunksOf74(t *testing.T) {
	b := refresherEnv(t, "backend-rf-chunks", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	ids := membershipIDs(160)
	b.setMembershipNodes(t, ids...)
	b.setShowMany(t, 0, summaryEntities(ids, time.Now()), nil) // each call answers every id; extras are ignored

	w := runChanges(t)
	if len(w.Changes) != 160 {
		t.Fatalf("changes = %d, want 160 added", len(w.Changes))
	}
	refs := b.showManyRefs(t)
	sizes := make([]int, len(refs))
	for i, r := range refs {
		sizes[i] = len(r)
	}
	if want := []int{74, 74, 12}; !equalInts(sizes, want) {
		t.Fatalf("show_many chunk sizes = %v, want %v", sizes, want)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0", n)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A failed chunk fails every id in it, and only those: the other chunk is still
// fetched, the pass is incomplete (no refreshed_at), and the next call asks
// again for exactly the ids that failed.
func TestChangesRefresher_AFailedChunkFailsItsIDsAndTheNextCallRetriesThem(t *testing.T) {
	b := refresherEnv(t, "backend-rf-chunkfail", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	ids := membershipIDs(100) // chunks: ids 1-74, ids 75-100
	b.setMembershipNodes(t, ids...)
	b.set(t, "show_many-error-1", "invalid_argument")
	b.setShowMany(t, 2, summaryEntities(ids[74:], time.Now()), nil)

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 26 || len(w.Changes) != 26 {
		t.Fatalf("changes = %d (%v), want only the 26 ids of the chunk that succeeded", len(w.Changes), changeKindsFor(w.Changes))
	}
	if !w.Sources[0].Truncated {
		t.Fatalf("source row = %+v, want truncated: the pass was incomplete", w.Sources[0])
	}
	l, _ := loadLedger(LedgerKey{Type: "pr", Backend: b.name, Query: "mine"})
	if l.RefreshedAt != nil || l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("RefreshedAt=%v LastError=%+v, want no refreshed_at and last_error truncated", l.RefreshedAt, l.LastError)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0: a failed chunk is not retried id by id", n)
	}

	// Next call: the 74 failed ids are asked for again and succeed; the 26 held ones are not.
	_ = os.Remove(b.dir + "/show_many-error-1")
	b.set(t, "show_many-3.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":13,"result":{"entities":[%s],"missing":[]}}`,
		strings.Join(summaryEntities(ids[:74], time.Now()), ",")))
	w = runChanges(t)
	if len(w.Changes) != 74 {
		t.Fatalf("retry changes = %d, want the 74 ids that failed", len(w.Changes))
	}
	refs := b.showManyRefs(t)
	if len(refs) != 3 || len(refs[2]) != 74 || refs[2][0].ID != ids[0] {
		t.Fatalf("retry requests = %+v, want one more call naming exactly the 74 failed ids", refs)
	}
	l, _ = loadLedger(LedgerKey{Type: "pr", Backend: b.name, Query: "mine"})
	if l.RefreshedAt == nil {
		t.Fatal("the completed retry must stamp refreshed_at")
	}
}

// An unavailable answer means the origin is down: the remaining chunks are not
// attempted and the pass is incomplete.
func TestChangesRefresher_AnUnavailableChunkStopsFurtherChunks(t *testing.T) {
	b := refresherEnv(t, "backend-rf-chunkdown", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	ids := membershipIDs(100)
	b.setMembershipNodes(t, ids...)
	b.set(t, "show_many-error-1", "unavailable")

	w := runChanges(t)
	if len(w.Changes) != 0 || !w.Sources[0].Truncated {
		t.Fatalf("changes=%+v source=%+v, want nothing delivered and a truncated source", w.Changes, w.Sources[0])
	}
	if n := len(b.showManyRefs(t)); n != 1 {
		t.Fatalf("show_many calls = %d, want 1: an unavailable origin stops the pass", n)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0", n)
	}
}

// A deleted or inaccessible node comes back null (reported missing): the id is
// confirmed with one show, which supplies its detail content when the entity
// still exists and leaves the pass incomplete when it does not.
func TestChangesRefresher_ANullNodeIsConfirmedWithShow(t *testing.T) {
	b := refresherEnv(t, "backend-rf-null", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	b.setMembershipNodes(t, "pr-1", "pr-2", "pr-3")
	b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t-pr-1", now)}, []string{"pr-2", "pr-3"})
	b.setEntity(t, "pr-2", "t-pr-2", now) // still readable with show
	// pr-3: show answers not_found (no script)

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 || len(w.Changes) != 2 {
		t.Fatalf("changes = %+v, want pr-1 and pr-2 added", w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("shows = %d, want 2 (one per null node)", n)
	}
	c := loadTestCache(t, b.name)
	if c.Entries["pr-1"].level() != CacheLevelSummary || c.Entries["pr-2"].level() != CacheLevelDetail {
		t.Fatalf("levels: pr-1=%q pr-2=%q, want summary and detail", c.Entries["pr-1"].level(), c.Entries["pr-2"].level())
	}
	if _, ok := c.Entries["pr-3"]; ok {
		t.Fatal("pr-3 never resolved and must not be cached")
	}
	if !w.Sources[0].Truncated {
		t.Fatalf("source row = %+v, want truncated: pr-3 could not be fetched", w.Sources[0])
	}
}

// An id the backend silently leaves out of both lists is treated as missing,
// and an entity it returns that was not asked for is ignored.
func TestChangesRefresher_AnIDTheBackendDroppedIsConfirmedAndAnUnaskedEntityIsIgnored(t *testing.T) {
	b := refresherEnv(t, "backend-rf-dropped", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	b.setMembershipNodes(t, "pr-1", "pr-2")
	b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t", now), summaryEntity("pr-stray", "stray", now)}, nil)
	b.setEntity(t, "pr-2", "t-pr-2", now)

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 || len(w.Changes) != 2 {
		t.Fatalf("changes = %+v, want pr-1 and pr-2 only", w.Changes)
	}
	for _, c := range w.Changes {
		if strings.Contains(string(c.Entity), "stray") {
			t.Fatalf("an entity that was not asked for was delivered: %s", c.Entity)
		}
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("shows = %d, want 1 (the dropped pr-2)", n)
	}
}

// Members the membership gave no node id for are fetched with show, the rest
// are batched.
func TestChangesRefresher_AMemberWithoutANodeIDIsFetchedWithShow(t *testing.T) {
	b := refresherEnv(t, "backend-rf-nonode", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t)
	now := time.Now()
	b.setMembershipNodesExcept(t, map[string]bool{"pr-2": true}, "pr-1", "pr-2")
	b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t", now)}, nil)
	b.setEntity(t, "pr-2", "t-pr-2", now)

	w := runChanges(t)
	if len(w.Changes) != 2 {
		t.Fatalf("changes = %+v, want both added", w.Changes)
	}
	refs := b.showManyRefs(t)
	if len(refs) != 1 || len(refs[0]) != 1 || refs[0][0].ID != "pr-1" {
		t.Fatalf("show_many requests = %+v, want only pr-1", refs)
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("shows = %d, want 1 for the member with no node id", n)
	}
}

// A backend that does not list show_many keeps the per-member show path, node
// ids or not.
func TestChangesRefresher_WithoutTheOpTheRefresherUsesShow(t *testing.T) {
	b := refresherEnv(t, "backend-rf-noop", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "t", now)
	b.setEntity(t, "pr-2", "t", now)
	b.setMembershipNodes(t, "pr-1", "pr-2")

	w := runChanges(t)
	if len(w.Changes) != 2 {
		t.Fatalf("changes = %+v, want both added", w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("shows = %d, want 2", n)
	}
	if n := len(b.showManyRefs(t)); n != 0 {
		t.Fatalf("show_many calls = %d, want 0 for a backend without the op", n)
	}
}

// A backend whose capabilities list show_many but which answers unknown_op (a
// stale answer) falls back to show for the chunk, and does not fail the pass.
func TestChangesRefresher_AnUnknownOpAnswerFallsBackToShow(t *testing.T) {
	b := refresherEnv(t, "backend-rf-staleops", "  cache_refresh_after: 2m\n")
	b.enableShowMany(t) // listed, but no show_many script: the backend answers unknown_op
	now := time.Now()
	b.setEntity(t, "pr-1", "t", now)
	b.setMembershipNodes(t, "pr-1")

	w := runChanges(t)
	if len(w.Changes) != 1 || w.Sources[0].Truncated {
		t.Fatalf("changes=%+v source=%+v, want one added and a whole pass", w.Changes, w.Sources[0])
	}
	if n := b.calls(t, "show"); n != 1 {
		t.Fatalf("shows = %d, want 1", n)
	}
}

// In the summary mode a member new to the ledger is fetched even when the cache
// holds a young detail entry for it: detail content hashes differently from the
// summary the rest of the pass contributes, so reusing it would later report a
// change that never happened. In the show mode the held detail is reused.
func TestChangesRefresher_NewMemberWithAYoungDetailEntry(t *testing.T) {
	for _, batched := range []bool{false, true} {
		t.Run(fmt.Sprintf("batched=%v", batched), func(t *testing.T) {
			b := refresherEnv(t, fmt.Sprintf("backend-rf-heldnew-%v", batched), "  cache_refresh_after: 2m\n")
			now := time.Now()
			if batched {
				b.enableShowMany(t)
				b.setShowMany(t, 0, []string{summaryEntity("pr-1", "t-pr-1", now)}, nil)
			}
			b.setEntity(t, "pr-1", "t-pr-1", now)
			b.setMembershipNodes(t, "pr-1")
			c := newEmptyCache()
			c.PutLevel("pr-1", json.RawMessage(summaryEntity("pr-1", "held", now)), now, now, CacheLevelDetail)
			seedCache(t, CacheKey{Type: "pr", Backend: b.name}, c)

			w := runChanges(t)
			if len(w.Changes) != 1 {
				t.Fatalf("changes = %+v, want pr-1 added", w.Changes)
			}
			held := strings.Contains(string(w.Changes[0].Entity), "held")
			if held == batched {
				t.Fatalf("entity = %s, batched=%v: the held detail must be reused only in the show mode", w.Changes[0].Entity, batched)
			}
		})
	}
}

// show_many is a pr op: an issue backend never gets one.
func TestBackendHasShowMany_OnlyForPr(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := newScriptedBackend(t, "backend-rf-hasop")
	b.enableShowMany(t)
	reg := registryFor(t, "backend-rf-hasop", "")
	ctx := context.Background()
	if !backendHasShowMany(ctx, reg, "pr", "backend-rf-hasop") {
		t.Error("pr backend listing show_many must be batched")
	}
	if backendHasShowMany(ctx, reg, "issue", "backend-rf-hasop") {
		t.Error("issue must never use show_many")
	}
	if backendHasShowMany(ctx, reg, "pr", "backend-not-registered") {
		t.Error("a capabilities failure must read as no show_many, not as a failure")
	}
}

// --- Cache: the refresher stamp -------------------------------------------

func TestCacheRefreshYoung_StampSemantics(t *testing.T) {
	now := time.Now()
	after := 2 * time.Minute
	old, fresh := now.Add(-time.Hour), now.Add(-time.Second)
	cases := []struct {
		name  string
		entry CacheEntry
		want  bool
	}{
		{"detail within after", CacheEntry{Level: CacheLevelDetail, AsOf: fresh}, true},
		{"detail aged", CacheEntry{Level: CacheLevelDetail, AsOf: old}, false},
		{"detail aged but stamped fresh", CacheEntry{Level: CacheLevelDetail, AsOf: old, RefreshedAt: &fresh}, true},
		{"summary fresh as_of but never stamped", CacheEntry{Level: CacheLevelSummary, AsOf: fresh}, false},
		{"summary stamped fresh", CacheEntry{Level: CacheLevelSummary, AsOf: old, RefreshedAt: &fresh}, true},
		{"summary stamp aged", CacheEntry{Level: CacheLevelSummary, AsOf: fresh, RefreshedAt: &old}, false},
		{"legacy entry (no level, no stamp)", CacheEntry{AsOf: fresh}, false},
		{"tombstoned", CacheEntry{Level: CacheLevelDetail, AsOf: fresh, RemovedAt: &fresh}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Cache{Entries: map[string]CacheEntry{"a": tc.entry}}
			if got := c.RefreshYoung("a", after, now); got != tc.want {
				t.Fatalf("RefreshYoung = %v, want %v", got, tc.want)
			}
		})
	}
	if newEmptyCache().RefreshYoung("absent", after, now) {
		t.Fatal("an absent entry is never young")
	}
}

// PutRefreshed stamps without changing what PutLevel decides, never moves a
// stamp backwards, and the stamp survives a later content replacement and a JSON
// round trip.
func TestCachePutRefreshed(t *testing.T) {
	c := newEmptyCache()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	t1 := t0.Add(30 * time.Minute)

	c.PutLevel("a", json.RawMessage(`{"id":"a","detail":true}`), t0, t0, CacheLevelDetail)
	c.PutRefreshed("a", json.RawMessage(`{"id":"a","summary":true}`), t1, t1, CacheLevelSummary)
	e := c.Entries["a"]
	if e.level() != CacheLevelDetail || !strings.Contains(string(e.Content), `"detail":true`) || !e.AsOf.Equal(t0) {
		t.Fatalf("a refreshed summary changed a live detail entry: %+v", e)
	}
	if e.RefreshedAt == nil || !e.RefreshedAt.Equal(t1) {
		t.Fatalf("RefreshedAt = %v, want %v", e.RefreshedAt, t1)
	}

	c.PutRefreshed("a", json.RawMessage(`{"id":"a","summary":true}`), t0, t1, CacheLevelSummary)
	if !c.Entries["a"].RefreshedAt.Equal(t1) {
		t.Fatalf("an older write moved the stamp backwards to %v", c.Entries["a"].RefreshedAt)
	}

	// A plain list write replaces a summary entry's content and keeps the stamp.
	c.PutRefreshed("s", json.RawMessage(`{"id":"s","v":1}`), t1, t1, CacheLevelSummary)
	c.PutLevel("s", json.RawMessage(`{"id":"s","v":2}`), t1.Add(time.Minute), t1.Add(time.Minute), CacheLevelSummary)
	if e := c.Entries["s"]; !strings.Contains(string(e.Content), `"v":2`) || e.RefreshedAt == nil || !e.RefreshedAt.Equal(t1) {
		t.Fatalf("entry = %+v, want new content and the old stamp kept", e)
	}
	// A tombstone does not carry its stamp into the entity's next life.
	c.Remove("s", t1)
	c.PutLevel("s", json.RawMessage(`{"id":"s","v":3}`), t1, t1, CacheLevelSummary)
	if c.Entries["s"].RefreshedAt != nil {
		t.Fatalf("a re-added entity carried the stamp of its tombstoned life: %+v", c.Entries["s"])
	}

	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Cache
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Entries["a"].RefreshedAt; got == nil || !got.Equal(t1) {
		t.Fatalf("stamp lost across a round trip: %v", got)
	}
	// An entry with no stamp omits the key, so existing cache files are unchanged.
	if strings.Contains(string(mustMarshal(t, CacheEntry{AsOf: t0})), "refreshed_at") {
		t.Fatal("an unstamped entry must omit refreshed_at")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
