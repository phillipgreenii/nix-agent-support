package store

import (
	"errors"
	"fmt"
	"testing"
)

const (
	fmInstance = "jira-main"
	fmAt       = "2026-10-26T00:00:00Z"
)

func fmEntity(id, facts string) Entity {
	return Entity{Repo: gateRepo, EntityType: gateIssueTyp, EntityID: id, Facts: facts, AsOf: fmAt, ContentHash: "h-" + facts}
}

func fmMarker(identity string, seq int64) *FeedMarker {
	return &FeedMarker{Instance: fmInstance, StoreIdentity: identity, Seq: seq}
}

// fmWrite writes one feed row of entity id (expectedVersion read from the
// store) with a marker, appending one change row.
func fmWrite(t *testing.T, s *Store, id string, m *FeedMarker) int64 {
	t.Helper()
	var expected int64
	if cur, found, err := s.GetEntity(gateRepo, gateIssueTyp, id); err != nil {
		t.Fatal(err)
	} else if found {
		expected = cur.Version
	}
	v, err := s.WriteEntityStateWithLogOpts(
		fmEntity(id, fmt.Sprintf("v%d", expected+1)), expected, fmAt, true, nil,
		[]string{"fields_changed"}, gateOrigin, fmAt, WriteOptions{FeedMarker: m},
	)
	if err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
	return v
}

func fmStoredMarker(t *testing.T, s *Store) (FeedMarker, bool) {
	t.Helper()
	m, found, err := s.FeedMarker(fmInstance)
	if err != nil {
		t.Fatal(err)
	}
	return m, found
}

func TestFeedMarker_WrittenWithEntityWriteAsJSON(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, found := fmStoredMarker(t, s); found {
		t.Fatal("marker found before any write")
	}
	fmWrite(t, s, "PROJ-1", fmMarker("X", 5))
	raw, found, err := s.GetMeta("change_flow.feed_seq." + fmInstance)
	if err != nil || !found {
		t.Fatalf("GetMeta = (%q, %v, %v)", raw, found, err)
	}
	if raw != `{"store_identity":"X","seq":5}` {
		t.Errorf("meta value = %s", raw)
	}
	m, found := fmStoredMarker(t, s)
	if !found || m != (FeedMarker{Instance: fmInstance, StoreIdentity: "X", Seq: 5}) {
		t.Errorf("FeedMarker = (%+v, %v)", m, found)
	}
	if n := changeCount(t, s); n != 1 {
		t.Errorf("change_log rows = %d, want 1", n)
	}
}

// TestFeedMarker_NilMarkerWritesNone: a write without a marker leaves the
// marker key absent.
func TestFeedMarker_NilMarkerWritesNone(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	fmWrite(t, s, "PROJ-1", nil)
	if _, found := fmStoredMarker(t, s); found {
		t.Error("a write with a nil FeedMarker stored a marker")
	}
}

// TestFeedMarker_RedeliveryIsIdempotent is the store half of T-22: after a
// crash (no ack) the batch is redelivered and every seq at or below the
// marker is skipped, so exactly one row exists per feed seq.
func TestFeedMarker_RedeliveryIsIdempotent(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	type row struct {
		seq int64
		id  string
	}
	batch := []row{{3, "PROJ-3"}, {4, "PROJ-4"}, {5, "PROJ-5"}}
	deliver := func() (written int) {
		for _, r := range batch {
			skip, err := s.ShouldSkipFeedRow(fmInstance, "X", r.seq, r.id)
			if err != nil {
				t.Fatal(err)
			}
			if skip {
				continue
			}
			fmWrite(t, s, r.id, fmMarker("X", r.seq))
			written++
		}
		return written
	}
	if got := deliver(); got != 3 {
		t.Fatalf("first delivery wrote %d rows, want 3", got)
	}
	// Crash before the ack: the same batch arrives again.
	if got := deliver(); got != 0 {
		t.Fatalf("redelivery wrote %d rows, want 0", got)
	}
	if n := changeCount(t, s); n != 3 {
		t.Errorf("change_log rows = %d, want exactly one per feed seq (3)", n)
	}
	for _, r := range batch {
		e, found, err := s.GetEntity(gateRepo, gateIssueTyp, r.id)
		if err != nil || !found || e.Version != 1 {
			t.Errorf("%s = (%+v, %v, %v), want version 1", r.id, e, found, err)
		}
	}
	if m, _ := fmStoredMarker(t, s); m.Seq != 5 {
		t.Errorf("marker seq = %d, want 5", m.Seq)
	}
	// A higher seq and a different identity are not redeliveries.
	for _, c := range []struct {
		identity string
		seq      int64
	}{{"X", 6}, {"Y", 1}, {"Y", 5}} {
		if skip, err := s.ShouldSkipFeedRow(fmInstance, c.identity, c.seq, "PROJ-9"); err != nil || skip {
			t.Errorf("ShouldSkipFeedRow(%s, %d) = (%v, %v), want false", c.identity, c.seq, skip, err)
		}
	}
	// Another instance has its own marker.
	if skip, err := s.ShouldSkipFeedRow("other", "X", 3, "PROJ-3"); err != nil || skip {
		t.Errorf("other instance skipped = (%v, %v), want false", skip, err)
	}
}

func TestFeedMarker_ResetLetsRestartedSequenceThrough(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	fmWrite(t, s, "PROJ-1", fmMarker("X", 5))
	if skip, _ := s.ShouldSkipFeedRow(fmInstance, "X", 1, "PROJ-1"); !skip {
		t.Fatal("seq 1 under identity X must be skipped before the reset")
	}
	if err := s.ResetFeedMarker(fmInstance); err != nil {
		t.Fatal(err)
	}
	if _, found := fmStoredMarker(t, s); found {
		t.Error("marker still present after ResetFeedMarker")
	}
	if skip, err := s.ShouldSkipFeedRow(fmInstance, "Y", 1, "PROJ-1"); err != nil || skip {
		t.Errorf("identity Y seq 1 after reset = (%v, %v), want not skipped", skip, err)
	}
	// Even the old identity restarts from nothing after a reset.
	if skip, err := s.ShouldSkipFeedRow(fmInstance, "X", 1, "PROJ-1"); err != nil || skip {
		t.Errorf("seq 1 after reset = (%v, %v), want not skipped", skip, err)
	}
	// Resetting an absent marker is not an error.
	if err := s.ResetFeedMarker(fmInstance); err != nil {
		t.Errorf("second reset: %v", err)
	}
}

func TestFeedMarker_ForwardOnlyWithinIdentityAndReplacedForAnother(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	fmWrite(t, s, "PROJ-1", fmMarker("X", 5))
	fmWrite(t, s, "PROJ-2", fmMarker("X", 3)) // lower: unchanged
	if m, _ := fmStoredMarker(t, s); m.Seq != 5 || m.StoreIdentity != "X" {
		t.Errorf("after lower seq, marker = %+v, want X/5", m)
	}
	fmWrite(t, s, "PROJ-3", fmMarker("X", 5)) // equal: unchanged
	if m, _ := fmStoredMarker(t, s); m.Seq != 5 {
		t.Errorf("after equal seq, marker = %+v", m)
	}
	fmWrite(t, s, "PROJ-4", fmMarker("X", 8))
	if m, _ := fmStoredMarker(t, s); m.Seq != 8 {
		t.Errorf("after higher seq, marker = %+v, want seq 8", m)
	}
	fmWrite(t, s, "PROJ-5", fmMarker("Y", 1)) // other identity replaces, even lower
	if m, _ := fmStoredMarker(t, s); m.StoreIdentity != "Y" || m.Seq != 1 {
		t.Errorf("after identity change, marker = %+v, want Y/1", m)
	}
}

func TestFeedMarker_FailedIngestEntrySkipsOnlySameIdentity(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.RecordIngestFailure(IngestFailure{
		Instance: fmInstance, EntityID: "PROJ-7", Seq: 7, StoreIdentity: "X",
		FirstFailedAt: fmAt, LastError: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		instance string
		identity string
		seq      int64
		entity   string
		want     bool
	}{
		{"same identity, same key", fmInstance, "X", 7, "PROJ-7", true},
		{"other identity, same seq", fmInstance, "Y", 7, "PROJ-7", false},
		{"other entity", fmInstance, "X", 7, "PROJ-8", false},
		{"other seq", fmInstance, "X", 8, "PROJ-7", false},
		{"other instance", "other", "X", 7, "PROJ-7", false},
	} {
		got, err := s.ShouldSkipFeedRow(c.instance, c.identity, c.seq, c.entity)
		if err != nil || got != c.want {
			t.Errorf("%s: ShouldSkipFeedRow = (%v, %v), want %v", c.name, got, err, c.want)
		}
	}
}

// TestFeedMarker_CommitsInOneTransaction injects a failure after the append
// and before the marker write: neither the row, the entity write nor the
// marker may persist.
func TestFeedMarker_CommitsInOneTransaction(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	fmWrite(t, s, "PROJ-1", fmMarker("X", 2))

	boom := errors.New("injected failure")
	afterAppendHook = func() error { return boom }
	t.Cleanup(func() { afterAppendHook = nil })
	_, err := s.WriteEntityStateWithLogOpts(
		fmEntity("PROJ-1", "next"), 1, fmAt, true, nil,
		[]string{"fields_changed"}, gateOrigin, fmAt, WriteOptions{FeedMarker: fmMarker("X", 3)},
	)
	afterAppendHook = nil
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if m, _ := fmStoredMarker(t, s); m.Seq != 2 {
		t.Errorf("marker = %+v, want unchanged seq 2", m)
	}
	if n := changeCount(t, s); n != 1 {
		t.Errorf("change_log rows = %d, want 1 (the failed write's row must not persist)", n)
	}
	e, _, _ := s.GetEntity(gateRepo, gateIssueTyp, "PROJ-1")
	if e.Version != 1 || e.Facts != "v1" {
		t.Errorf("entity = %+v, want version 1 facts v1", e)
	}
	// The failed attempt left the store usable.
	fmWrite(t, s, "PROJ-1", fmMarker("X", 3))
	if m, _ := fmStoredMarker(t, s); m.Seq != 3 {
		t.Errorf("marker after retry = %+v, want seq 3", m)
	}
}

// TestFeedMarker_GatedAndEmptyKindsWritesStillCarryMarker: the marker commits
// even when the delivery gate withholds the row or no row is appended.
func TestFeedMarker_GatedAndEmptyKindsWritesStillCarryMarker(t *testing.T) {
	s := gateStore(t)
	backend := gateOwning
	// Gated: owning backend, not watched, so no row is appended.
	v, err := s.WriteEntityStateWithLogOpts(
		fmEntity("PROJ-1", "a"), 0, fmAt, true, nil, []string{"created"}, gateOrigin, fmAt,
		WriteOptions{Backend: &backend, FeedMarker: fmMarker("X", 4)},
	)
	if err != nil || v != 1 {
		t.Fatalf("gated write = (%d, %v)", v, err)
	}
	if n := changeCount(t, s); n != 0 {
		t.Errorf("change_log rows = %d, want 0 (gated)", n)
	}
	if m, _ := fmStoredMarker(t, s); m.Seq != 4 {
		t.Errorf("marker after gated write = %+v, want seq 4", m)
	}
	// Empty kinds: no row, marker still moves.
	if _, err := s.WriteEntityStateWithLogOpts(
		fmEntity("PROJ-1", "b"), 1, fmAt, true, nil, nil, gateOrigin, fmAt,
		WriteOptions{FeedMarker: fmMarker("X", 6)},
	); err != nil {
		t.Fatal(err)
	}
	if m, _ := fmStoredMarker(t, s); m.Seq != 6 {
		t.Errorf("marker after empty-kinds write = %+v, want seq 6", m)
	}
}

func TestFeedMarker_VersionConflictLeavesMarkerUnchanged(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	fmWrite(t, s, "PROJ-1", fmMarker("X", 2))
	_, err := s.WriteEntityStateWithLogOpts(
		fmEntity("PROJ-1", "stale"), 0, fmAt, true, nil, []string{"x"}, gateOrigin, fmAt,
		WriteOptions{FeedMarker: fmMarker("X", 9)},
	)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if m, _ := fmStoredMarker(t, s); m.Seq != 2 {
		t.Errorf("marker = %+v, want unchanged seq 2", m)
	}
}

func TestAdvanceFeedMarker(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	adv := func(identity string, seq int64) {
		t.Helper()
		if err := s.AdvanceFeedMarker(FeedMarker{Instance: fmInstance, StoreIdentity: identity, Seq: seq}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(identity string, seq int64) {
		t.Helper()
		m, found := fmStoredMarker(t, s)
		if !found || m.StoreIdentity != identity || m.Seq != seq {
			t.Errorf("marker = (%+v, %v), want %s/%d", m, found, identity, seq)
		}
	}
	adv("X", 4)
	check("X", 4)
	adv("X", 2) // backward: unchanged
	check("X", 4)
	adv("X", 4) // equal: unchanged
	check("X", 4)
	adv("X", 7)
	check("X", 7)
	adv("Y", 1) // different identity replaces
	check("Y", 1)

	if n := changeCount(t, s); n != 0 {
		t.Errorf("change_log rows = %d, want 0", n)
	}
	var entities int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM entity`).Scan(&entities); err != nil || entities != 0 {
		t.Errorf("entity rows = (%d, %v), want 0", entities, err)
	}
	// The advanced marker drives the skip rule.
	if skip, _ := s.ShouldSkipFeedRow(fmInstance, "Y", 1, "PROJ-1"); !skip {
		t.Error("seq at the advanced marker must be skipped")
	}
}

func TestFeedMarker_CorruptMarkerIsAnError(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.SetMeta("change_flow.feed_seq."+fmInstance, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FeedMarker(fmInstance); err == nil {
		t.Error("FeedMarker on corrupt value returned nil error")
	}
	if _, err := s.ShouldSkipFeedRow(fmInstance, "X", 1, "PROJ-1"); err == nil {
		t.Error("ShouldSkipFeedRow on corrupt marker returned nil error")
	}
	if err := s.ResetFeedMarker(fmInstance); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.FeedMarker(fmInstance); err != nil || found {
		t.Errorf("after reset = (%v, %v)", found, err)
	}
}
