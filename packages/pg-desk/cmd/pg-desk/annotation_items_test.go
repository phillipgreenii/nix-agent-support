package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const annotationsConsumer = "annotations:pr-lane"

func runAnnotationItemsCmd(t *testing.T, entityType string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return runGroupCmd(t, newAnnotationItemsCmd, entityType, args...)
}

func itemsOf(t *testing.T, out string) []annotationItem {
	t.Helper()
	var items []annotationItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("stdout is not an item array: %v\n%s", err, out)
	}
	return items
}

// annotate writes one annotation through the store with the origin given.
func annotate(t *testing.T, st *store.Store, typ, id, key, value, origin string) {
	t.Helper()
	if _, _, err := st.SetAnnotationSeq(store.KVAnnotation{
		Repo: "o/r", EntityType: typ, EntityID: id, Key: key, Value: value, Origin: origin, SetBy: "alice", SetAt: "2026-10-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

// hydrate appends one non-annotation (hydration) change row.
func hydrate(t *testing.T, st *store.Store, typ, id string) {
	t.Helper()
	cur, found, err := st.GetEntity("o/r", typ, id)
	if err != nil || !found {
		t.Fatalf("entity %s %s: found=%v err=%v", typ, id, found, err)
	}
	if _, err := st.WriteEntityWithLog(
		store.Entity{Repo: "o/r", EntityType: typ, EntityID: id, Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
		cur.Version, []string{"refreshed"}, "sync", "2026-09-29T10:00:00Z",
	); err != nil {
		t.Fatal(err)
	}
}

func cursorOf(t *testing.T, st *store.Store, name, typ string) int64 {
	t.Helper()
	cs, err := st.ListConsumers()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name && c.Type == typ {
			return c.Cursor
		}
	}
	t.Fatalf("consumer %s/%s not registered", name, typ)
	return 0
}

// First registration starts at the current max: records that exist already
// are not replayed; only later annotation changes are delivered.
func TestAnnotationItemsFirstRegistrationStartsAtCurrentMax(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	withOpenSeams(t, linkTestConfig(), open)
	annotate(t, seed, "pr", "o/r#1", "wip", "true", "pg-desk")

	out, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("first poll = %q, want [] (pre-existing records are not replayed)", out)
	}
	annotate(t, seed, "pr", "o/r#1", "wip", "false", "pg-desk")
	out, err = runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemsOf(t, out); len(got) != 1 {
		t.Fatalf("second poll = %s, want one item", out)
	}
}

// The item shape: only annotation_changed rows of the type after the cursor,
// ascending, one per record (no merging), origin carried, digest stable per
// record and distinct across records, no metadata.seq, no expiresAt without
// --retry-window.
func TestAnnotationItemsShapeOrderAndKindFilter(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"}, [2]string{"pr", "o/r#2"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer); err != nil { // register at max
		t.Fatal(err)
	}
	annotate(t, seed, "pr", "o/r#1", "wip", "true", "pg-desk")
	hydrate(t, seed, "pr", "o/r#2")
	annotate(t, seed, "issue", "K-1", "wip", "true", "pg-desk") // another type
	annotate(t, seed, "pr", "o/r#1", "suppress.lint", "true", "decider:pr-decider")
	annotate(t, seed, "pr", "o/r#1", "wip", "false", "pg-desk")

	out, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer)
	if err != nil {
		t.Fatal(err)
	}
	items := itemsOf(t, out)
	if len(items) != 3 {
		t.Fatalf("items = %s, want three (one per annotation record of pr, no merging)", out)
	}
	wantOrigins := []string{"pg-desk", "decider:pr-decider", "pg-desk"}
	seen := map[string]bool{}
	for i, it := range items {
		if it.Type != "pr.changed" {
			t.Errorf("item %d type = %q, want pr.changed", i, it.Type)
		}
		if it.Metadata["origin"] != wantOrigins[i] || it.Metadata["entity_id"] != "o/r#1" {
			t.Errorf("item %d metadata = %v, want origin %q entity o/r#1", i, it.Metadata, wantOrigins[i])
		}
		if k, _ := it.Metadata["kinds"].([]any); len(k) != 1 || k[0] != "annotation_changed" {
			t.Errorf("item %d kinds = %v", i, it.Metadata["kinds"])
		}
		if _, has := it.Metadata["seq"]; has {
			t.Errorf("item %d has metadata.seq; it would share the delivery-key space with the daemon's seq", i)
		}
		if it.ExpiresAt != "" {
			t.Errorf("item %d expiresAt = %q without --retry-window", i, it.ExpiresAt)
		}
		if !strings.HasPrefix(it.ID, "o/r#1@") || len(it.ID) != len("o/r#1@")+12 {
			t.Errorf("item %d id = %q, want o/r#1@<12 hex>", i, it.ID)
		}
		if seen[it.ID] {
			t.Errorf("id %q repeats across records", it.ID)
		}
		seen[it.ID] = true
	}
	// The raw JSON carries no seq member at all.
	if strings.Contains(out, `"seq"`) {
		t.Errorf("output mentions seq: %s", out)
	}
}

// The digest of a record is stable (same inputs, same id) and the zero digest
// is remapped, because 000000000000 reads as "no key" to pg-pr-change.
func TestAnnotationItemDigestStableAndZeroRemapped(t *testing.T) {
	a := annotationItemDigest("pr", "o/r#1", 7, "pg-desk")
	if b := annotationItemDigest("pr", "o/r#1", 7, "pg-desk"); a != b {
		t.Errorf("digest not stable: %s vs %s", a, b)
	}
	for _, other := range []string{
		annotationItemDigest("pr", "o/r#1", 8, "pg-desk"),
		annotationItemDigest("pr", "o/r#2", 7, "pg-desk"),
		annotationItemDigest("pr", "o/r#1", 7, "decider:pr-decider"),
		annotationItemDigest("issue", "o/r#1", 7, "pg-desk"),
	} {
		if other == a {
			t.Errorf("digest %s collides across differing inputs", a)
		}
	}
	if got := remapZeroDigest("000000000000"); got != "000000000001" {
		t.Errorf("zero digest remapped to %q, want 000000000001", got)
	}
	if got := remapZeroDigest("00000000000a"); got != "00000000000a" {
		t.Errorf("non-zero digest changed to %q", got)
	}
}

func TestAnnotationItemsExpiresAtOnlyWithRetryWindow(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	withOpenSeams(t, linkTestConfig(), open)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	orig := annotationItemsNow
	annotationItemsNow = func() time.Time { return now }
	t.Cleanup(func() { annotationItemsNow = orig })

	if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer); err != nil {
		t.Fatal(err)
	}
	annotate(t, seed, "pr", "o/r#1", "wip", "true", "pg-desk")
	out, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer, "--retry-window", "10m")
	if err != nil {
		t.Fatal(err)
	}
	items := itemsOf(t, out)
	if len(items) != 1 || items[0].ExpiresAt != "2026-10-01T12:10:00Z" {
		t.Errorf("items = %s, want one item expiring at 2026-10-01T12:10:00Z", out)
	}
}

// The cursor advances to the last row SCANNED: a page made only of
// non-annotation rows emits [] and still moves the cursor, so the feed cannot
// stall; lag is 0 afterwards.
func TestAnnotationItemsCursorAdvancesPastNonAnnotationRows(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		hydrate(t, seed, "pr", "o/r#1")
	}
	out, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer, "--limit", "3")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("poll = %q, want []", out)
	}
	first := cursorOf(t, seed, annotationsConsumer, "pr")
	max := maxSeqOf(t, seed, "pr")
	if first >= max {
		t.Fatalf("cursor %d after a limit-3 page of 5 rows, want it short of max %d", first, max)
	}
	if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer, "--limit", "3"); err != nil {
		t.Fatal(err)
	}
	if got := cursorOf(t, seed, annotationsConsumer, "pr"); got != max {
		t.Errorf("cursor = %d after the second page, want max %d (lag 0)", got, max)
	}
}

func maxSeqOf(t *testing.T, st *store.Store, typ string) int64 {
	t.Helper()
	rows, err := st.ListChangesAfter(typ, 0, 0)
	if err != nil || len(rows) == 0 {
		t.Fatalf("no rows for %s: %v", typ, err)
	}
	return rows[len(rows)-1].Seq
}

func TestAnnotationItemsEmptyPollPrintsEmptyArray(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	withOpenSeams(t, linkTestConfig(), open)
	for i := 0; i < 2; i++ {
		out, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("poll %d = %q, want []", i, out)
		}
	}
}

// The annotations: prefix is required here and reserved against `changes`;
// an existing `<type> changes` consumer still starts at 0.
func TestAnnotationConsumerPrefixIsRequiredHereAndRefusedByChanges(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	for _, bad := range []string{"pr-lane", "annotations:", "Annotations:x"} {
		if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", bad); err == nil || !strings.Contains(err.Error(), annotationConsumerPrefix) {
			t.Errorf("consumer %q: error = %v, want the prefix refusal", bad, err)
		}
	}
	if _, _, err := runChangesCmd(t, "issue", "--consumer", "annotations:x", "--cached"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("changes with the reserved prefix: error = %v, want a refusal", err)
	}
	// RegisterConsumer (the `changes` path) still starts at 0; the new
	// registration starts at max.
	if err := seed.RegisterConsumer("router", "issue", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := cursorOf(t, seed, "router", "issue"); got != 0 {
		t.Errorf("changes consumer starts at %d, want 0", got)
	}
	if _, err := runAnnotationItemsCmd(t, "issue", "--consumer", annotationsConsumer); err != nil {
		t.Fatal(err)
	}
	if got, want := cursorOf(t, seed, annotationsConsumer, "issue"), maxSeqOf(t, seed, "issue"); got != want {
		t.Errorf("annotation consumer starts at %d, want max %d", got, want)
	}
}

func TestAnnotationItemsRefusesAnOldSchemaStore(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer)
	wantOldSchemaRefusal(t, err)
}

func TestAnnotationItemsIsRegisteredForEveryType(t *testing.T) {
	for _, typ := range typedEntityTypes {
		c, _, err := rootCmd.Find([]string{typ, "annotation-items"})
		if err != nil || c.Name() != "annotation-items" || c.Parent() != typeGroup(typ) {
			t.Errorf("%s annotation-items not registered: %v, %v", typ, c, err)
		}
	}
}

// A live annotation consumer holds back pruning of its type's rows; once it is
// stale (unseen for staleAfter) it no longer does.
func TestAnnotationConsumerHoldsBackPruningUntilStale(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runAnnotationItemsCmd(t, "pr", "--consumer", annotationsConsumer); err != nil {
		t.Fatal(err)
	}
	annotate(t, seed, "pr", "o/r#1", "wip", "true", "pg-desk") // undelivered
	future := time.Now().Add(30 * 24 * time.Hour)
	// Live: seen_at is now, so the undelivered row survives a prune past retention.
	if err := seed.RegisterConsumer(annotationsConsumer, "pr", future); err != nil {
		t.Fatal(err)
	}
	n, err := seed.PruneChangeLog(future, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := seed.ListChangesAfter("pr", cursorOf(t, seed, annotationsConsumer, "pr"), 0); len(rows) != 1 {
		t.Errorf("undelivered rows after prune (deleted %d) = %d, want 1 held back", n, len(rows))
	}
	// Stale: unseen for longer than staleAfter, so pruning stops waiting.
	if _, err := seed.PruneChangeLog(future.Add(10*24*time.Hour), 0, 0); err != nil {
		t.Fatal(err)
	}
	if rows, _ := seed.ListChangesAfter("pr", 0, 0); len(rows) != 0 {
		t.Errorf("rows left = %d, want 0 once the consumer is stale", len(rows))
	}
}
