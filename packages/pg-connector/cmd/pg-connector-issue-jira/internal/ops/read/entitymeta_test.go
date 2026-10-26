package read

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The watched flag as entity_meta on show and list answers (bead pg2-0o693):
// metadata beside the entity, read from the stored flag and never a cause of a
// version, a change row or an event.

// wantWatched fails unless the entity states exactly want.
func wantWatched(t *testing.T, what string, m *schema.EntityMeta, want bool) {
	t.Helper()
	got, stated := m.WatchedState()
	if !stated || got != want {
		t.Errorf("%s: entity_meta = %+v, want watched %v stated", what, m, want)
	}
}

// setWatchedDirect flips the stored flag the way a watched query run does,
// bypassing every read path, so a test can assert what the flip does NOT cause.
func (r *rig) setWatchedDirect(id string) {
	r.t.Helper()
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.SetWatched(context.Background(), groups.EntityKind, id, registry.ViaQuery+"mine", r.clk.Now())
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) head() int64 {
	r.t.Helper()
	h, err := r.st.ChangeHead(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *rig) version(id string) int64 {
	r.t.Helper()
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, id)
	if err != nil || !ok {
		r.t.Fatalf("entity %s: %v %v", id, ok, err)
	}
	return ent.Version
}

func TestShowStatesTheWatchedFlagFalseForAReadOnlyIssueAndTrueAfterAOneOffWatch(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")

	wantWatched(t, "read-only show", r.mustShow(`{"id":"ABC-1"}`).EntityMeta, false)
	wantWatched(t, "the watching show itself", r.mustShow(`{"id":"ABC-1","watch":true}`).EntityMeta, true)
	wantWatched(t, "a later read without the option", r.mustShow(`{"id":"ABC-1"}`).EntityMeta, true)

	// An issue first seen by a watching read is stated watched in that same answer.
	r.origin.set(issueOf("XYZ-7", "new one"), "its description")
	wantWatched(t, "first sighting with watch", r.mustShow(`{"id":"XYZ-7","watch":true}`).EntityMeta, true)
	// ... and one first seen by a plain read is stated not watched.
	r.origin.set(issueOf("XYZ-8", "other"), "its description")
	wantWatched(t, "first sighting without watch", r.mustShow(`{"id":"XYZ-8"}`).EntityMeta, false)
	if r.isWatched("XYZ-8") {
		t.Error("exposing the flag watched an issue read without the option")
	}
}

func TestListStatesTheWatchedFlagOfEveryEntity(t *testing.T) {
	r := newRig(t)
	r.seedMembers() // ABC-1 and ABC-2 in query mine
	r.setWatchedDirect("ABC-1")

	res := r.mustList(`{"query":"mine"}`)
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(res.Entities))
	}
	for _, e := range res.Entities {
		wantWatched(t, "list entity "+e.ID, e.EntityMeta, e.ID == "ABC-1")
	}
	if r.isWatched("ABC-2") {
		t.Error("listing watched a member")
	}

	// ids_only names no entity, so there is nothing to carry the flag.
	ids := r.mustList(`{"query":"mine","ids_only":true}`)
	if len(ids.Entities) != 0 || len(ids.PresentIDs) != 2 {
		t.Errorf("ids_only answer = %+v", ids)
	}
}

func TestTheWatchedFlagIsStickyAfterTheIssueLeavesEveryQueryAndClearsOnlyAtEviction(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.setWatchedDirect("ABC-1")

	// ABC-1 leaves the query: the stored row now names only ABC-2.
	r.putQuery("mine", true, r.clk.Now(), "ABC-2")
	res := r.mustList(`{"query":"mine"}`)
	if len(res.Entities) != 1 || res.Entities[0].ID != "ABC-2" {
		t.Fatalf("entities = %+v, want only ABC-2", res.Entities)
	}
	wantWatched(t, "the issue outside every query", r.mustShow(`{"id":"ABC-1"}`).EntityMeta, true)

	// It closes; before the eviction age the flag is still set.
	closedAt := r.clk.Now()
	if err := r.st.SetClosedAt(context.Background(), groups.EntityKind, "ABC-1", closedAt); err != nil {
		t.Fatal(err)
	}
	if n, err := summary.Sweep(context.Background(), r.st, testExpiry, closedAt.Add(testExpiry-time.Second)); err != nil || n != 0 {
		t.Fatalf("early sweep evicted %d (%v), want 0", n, err)
	}
	wantWatched(t, "closed, inside the eviction age", r.mustShow(`{"id":"ABC-1"}`).EntityMeta, true)

	// Eviction deletes the row, and with it the flag.
	if n, err := summary.Sweep(context.Background(), r.st, testExpiry, closedAt.Add(testExpiry)); err != nil || n != 1 {
		t.Fatalf("sweep evicted %d (%v), want 1", n, err)
	}
	if r.isWatched("ABC-1") {
		t.Fatal("the flag survived the eviction")
	}
	r.origin.set(issueOf("ABC-1", "again"), "d")
	wantWatched(t, "the evicted issue read again", r.mustShow(`{"id":"ABC-1"}`).EntityMeta, false)
}

func TestFlippingTheWatchedFlagChangesNoVersionAndAppendsNoRow(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.mustShow(`{"id":"ABC-1"}`) // warm: both answers below are served from the cache
	before := r.mustShow(`{"id":"ABC-1"}`)
	wantWatched(t, "before the flip", before.EntityMeta, false)
	versionBefore, headBefore := r.version("ABC-1"), r.head()

	r.setWatchedDirect("ABC-1")

	after := r.mustShow(`{"id":"ABC-1"}`)
	wantWatched(t, "after the flip", after.EntityMeta, true)
	if after.Version != before.Version || r.version("ABC-1") != versionBefore {
		t.Errorf("version %d -> %d (stored %d -> %d), want unchanged", before.Version, after.Version, versionBefore, r.version("ABC-1"))
	}
	if r.head() != headBefore || len(r.rows("ABC-1")) != 0 {
		t.Errorf("change head %d -> %d, rows %+v, want no row from the flip", headBefore, r.head(), r.rows("ABC-1"))
	}

	// The metadata is outside the content: the two answers are the same entity
	// once the metadata is dropped.
	before.EntityMeta, after.EntityMeta = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the entity content differs across the flip:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestReadingTheFlagNeverSetsOrClearsIt(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.setWatchedDirect("ABC-2")
	head := r.head()
	for i := 0; i < 2; i++ {
		r.mustShow(`{"id":"ABC-1"}`)
		r.mustShow(`{"id":"ABC-2"}`)
		r.mustList(`{"query":"mine"}`)
	}
	if r.isWatched("ABC-1") || !r.isWatched("ABC-2") || r.head() != head {
		t.Errorf("watched ABC-1=%v ABC-2=%v head %d -> %d, want the flags and the log untouched",
			r.isWatched("ABC-1"), r.isWatched("ABC-2"), head, r.head())
	}
}

func TestAnAnswerWithoutAStoreStatesNothingAboutTheFlag(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.ops = Handlers(Deps{Summary: r.summary, Detail: r.detail, Clock: r.clk})
	iss := r.mustShow(`{"id":"ABC-1"}`)
	if iss.EntityMeta != nil {
		t.Errorf("entity_meta = %+v, want none when the store was not consulted", iss.EntityMeta)
	}
	raw, err := json.Marshal(iss)
	if err != nil || strings.Contains(string(raw), "entity_meta") {
		t.Errorf("wire answer %s (%v) carries entity_meta, want it omitted", raw, err)
	}
}
