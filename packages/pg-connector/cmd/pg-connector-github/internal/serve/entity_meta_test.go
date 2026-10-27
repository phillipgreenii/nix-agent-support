//go:build integration

package serve_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The watched flag as entity_meta on show and list answers (bead pg2-xxcdow),
// with the real daemon binary over the fake gh: metadata beside the entity,
// read from the stored flag, never a cause of a version, a change row or an
// event. The proofs of the stored flag's own rules (set by a watched query or a
// watching read, sticky, cleared at eviction) are the shared library's; these
// pin that the GitHub daemon states it on the wire.

// prT is listed by the unwatched team query only, so it is tracked, never
// watched, until something watches it.
var prT = servetest.PR{NodeID: "PR_node_t", Repo: "example-org/gadget", Number: 19}

// seedEntityMeta is seedUnqueried with the team query listing prT beside prA
// and prB (mine, the watched query, lists prA, prB and prC); prW is known to no
// query.
func seedEntityMeta(d *servetest.Daemon) {
	seedUnqueried(d)
	d.Shim.SetPRs(servetest.TeamExpr, prA, prB, prT)
}

// metaShow is show with the answer decoded, and the raw answer for wire checks.
func metaShow(t *testing.T, d *servetest.Daemon, id string, watch bool) (schema.PR, json.RawMessage) {
	t.Helper()
	args := map[string]any{"id": id}
	if watch {
		args["watch"] = true
	}
	raw := callOK(t, d, "pr", "show", args)
	return decode[schema.PR](t, raw), raw
}

// wantWatched fails unless the entity states exactly want.
func wantWatched(t *testing.T, what string, m *schema.EntityMeta, want bool) {
	t.Helper()
	got, stated := m.WatchedState()
	if !stated || got != want {
		t.Errorf("%s: entity_meta = %+v, want watched %v stated", what, m, want)
	}
}

// metaDaemon starts the daemon with the mine query watched and re-run every
// second, and waits until both queries hold their baseline.
func metaDaemon(t *testing.T, opts servetest.Options) *servetest.Daemon {
	t.Helper()
	opts.Before = seedEntityMeta
	if opts.Config == nil {
		opts.Config = watchedFlipConfig()
	}
	d := servetest.NewDaemon(t, opts)
	d.Start(t)
	settlePRs(t, d, 4)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	return d
}

func TestEntityMeta_ShowAndListStateTheStoredFlagAndAReadWithoutWatchSetsNothing(t *testing.T) {
	d := metaDaemon(t, servetest.Options{})
	db := d.OpenStore(t)
	rowsBefore := countRows(t, d, "change_log")

	// list: every member of the watched query, and every member of the team
	// query that mine also lists, is stated watched; the team-only member is
	// stated NOT watched (tracked only).
	mine := mustList(t, d, servetest.QueryMine, nil)
	if len(mine.Entities) != 3 {
		t.Fatalf("mine entities = %d, want 3", len(mine.Entities))
	}
	for _, e := range mine.Entities {
		wantWatched(t, "list mine "+e.ID, e.EntityMeta, true)
	}
	team := mustList(t, d, servetest.QueryTeam, nil)
	if len(team.Entities) != 3 {
		t.Fatalf("team entities = %d, want 3", len(team.Entities))
	}
	for _, e := range team.Entities {
		wantWatched(t, "list team "+e.ID, e.EntityMeta, e.ID != prT.Ref())
	}
	// ids_only names no entity, so there is nothing to carry the flag.
	if ids := mustList(t, d, servetest.QueryTeam, map[string]any{"ids_only": true}); len(ids.Entities) != 0 || len(ids.PresentIDs) != 3 {
		t.Errorf("ids_only answer = %+v", ids)
	}

	// show: a member of a watched query, a tracked-only PR, and a PR the daemon
	// first meets through a plain read.
	shown, raw := metaShow(t, d, prA.Ref(), false)
	wantWatched(t, "show "+prA.Ref(), shown.EntityMeta, true)
	var wire struct {
		Meta map[string]any `json:"entity_meta"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Meta["watched"] != true {
		t.Errorf("wire entity_meta = %v (%v), want {watched: true} beside the entity", wire.Meta, err)
	}
	shown, _ = metaShow(t, d, prT.Ref(), false)
	wantWatched(t, "show "+prT.Ref()+" (tracked only)", shown.EntityMeta, false)
	shown, _ = metaShow(t, d, prW.Ref(), false)
	wantWatched(t, "show "+prW.Ref()+" (read only)", shown.EntityMeta, false)

	// The plain reads watched nothing, and stating the flag changed nothing.
	for _, p := range []servetest.PR{prT, prW} {
		if got := readEntity(t, db, p.NodeID); !got.exists || got.watched {
			t.Errorf("%s after plain reads = %+v, want tracked and not watched", p.Ref(), got)
		}
	}
	if got := countRows(t, d, "change_log"); got != rowsBefore {
		t.Errorf("change_log rows %d -> %d across reads that state the flag, want none", rowsBefore, got)
	}

	// A one-off watch read states the flag it just set (the answer is read
	// AFTER the watch was applied), and the flag is sticky across later reads.
	shown, _ = metaShow(t, d, prW.Ref(), true)
	wantWatched(t, "show "+prW.Ref()+" with watch", shown.EntityMeta, true)
	shown, _ = metaShow(t, d, prW.Ref(), false)
	wantWatched(t, "show "+prW.Ref()+" after the watch", shown.EntityMeta, true)
	// ... while the plain reads above left the tracked-only PR as it was.
	shown, _ = metaShow(t, d, prT.Ref(), false)
	wantWatched(t, "show "+prT.Ref()+" again", shown.EntityMeta, false)
}

func TestEntityMeta_TheFlagIsStickyAfterThePRLeavesEveryQuery(t *testing.T) {
	d := metaDaemon(t, servetest.Options{})
	shown, _ := metaShow(t, d, prC.Ref(), false)
	wantWatched(t, prC.Ref()+" as a member", shown.EntityMeta, true)

	// prC leaves mine (its node says MERGED for the confirming read) and is
	// listed by no other query.
	prCMerged := prC
	prCMerged.State = "MERGED"
	d.Shim.SetPRs("is:open label:unused-fixture", prCMerged)
	d.Shim.SetPRs(servetest.MineExpr, prA, prB)
	until(t, 60*time.Second, prC.Ref()+" to leave the mine query", func() bool {
		return !slices.Contains(mustList(t, d, servetest.QueryMine, nil).PresentIDs, prC.Ref())
	})
	shown, _ = metaShow(t, d, prC.Ref(), false)
	wantWatched(t, prC.Ref()+" after it left every query", shown.EntityMeta, true)
}

func TestEntityMeta_AFlipOfTheFlagChangesNoVersionNoRowAndNoEvent(t *testing.T) {
	d := metaDaemon(t, servetest.Options{})
	const consumer = "meta-flip"
	pollUnscoped(t, d, consumer) // the consumer starts at the tail

	before, _ := metaShow(t, d, prT.Ref(), false)
	wantWatched(t, "before the flip", before.EntityMeta, false)
	versionBefore := d.EntityVersion(t, "pr", prT.NodeID)
	rowsBefore, pendingBefore := len(d.ChangeLog(t)), len(d.PendingRows(t))

	// Flip the stored flag the way a watched query run does, bypassing every
	// read path, so the test sees what the flip itself causes.
	db := d.WritableStore(t)
	if _, err := db.Exec(`UPDATE entity SET watched_at = ?, watched_via = ? WHERE kind = 'pr' AND id = ?`,
		time.Now().UnixMilli(), "query:"+servetest.QueryTeam, prT.NodeID); err != nil {
		t.Fatal(err)
	}

	after, _ := metaShow(t, d, prT.Ref(), false)
	wantWatched(t, "after the flip", after.EntityMeta, true)
	if got := d.EntityVersion(t, "pr", prT.NodeID); got != versionBefore {
		t.Errorf("entity version %d -> %d across the flip, want unchanged", versionBefore, got)
	}
	if got := len(d.ChangeLog(t)); got != rowsBefore {
		t.Errorf("change_log rows %d -> %d across the flip, want none", rowsBefore, got)
	}
	if got := len(d.PendingRows(t)); got != pendingBefore {
		t.Errorf("pending rows %d -> %d across the flip, want none", pendingBefore, got)
	}
	if res := pollUnscoped(t, d, consumer); len(res.Changes) != 0 {
		t.Errorf("the feed delivered %v for a flip of the flag, want no event", changeWords(res))
	}

	// The metadata is outside the content: the two answers are the same entity
	// once it and the read annotations are dropped.
	for _, p := range []*schema.PR{&before, &after} {
		p.EntityMeta, p.AsOf, p.AgeSeconds, p.ServedFrom, p.Groups = nil, "", nil, "", nil
	}
	if !jsonEqual(t, before, after) {
		t.Errorf("the entity content differs across the flip:\nbefore %+v\nafter  %+v", before, after)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatalf("marshal: %v %v", err1, err2)
	}
	return string(x) == string(y)
}

func TestEntityMeta_TheFlagClearsAtEviction(t *testing.T) {
	// A short expiry and a fast sweep: a closed PR outside every query is
	// evicted once its stored groups are older than the expiry.
	d := metaDaemon(t, servetest.Options{
		Hooks: "sweep_interval=200ms",
		Config: func() map[string]any {
			c := watchedFlipConfig()
			c["expiry"] = "4s"
			return c
		}(),
	})
	db := d.OpenStore(t)

	// A PR the daemon had never seen is stated watched in the answer of the very
	// read that first stored it.
	shown, _ := metaShow(t, d, prW.Ref(), true)
	wantWatched(t, "first sighting with watch", shown.EntityMeta, true)
	if got := readEntity(t, db, prW.NodeID); !got.watched {
		t.Fatalf("control: %+v, want watched", got)
	}

	// The PR is merged at the origin and read fresh, so the stored summary says
	// closed and nothing keeps it alive.
	merged := prW
	merged.State = "MERGED"
	d.Shim.SetPRs("author:nobody", merged)
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref(), "fresh": true})

	until(t, 60*time.Second, prW.Ref()+" to be evicted", func() bool {
		return !readEntity(t, db, prW.NodeID).exists
	})
	// Read again, the PR is a new entity: the flag did not survive the eviction.
	d.Shim.SetPRs("author:nobody", prW)
	shown, _ = metaShow(t, d, prW.Ref(), false)
	wantWatched(t, "show after the eviction", shown.EntityMeta, false)
	if got := readEntity(t, db, prW.NodeID); !got.exists || got.watched {
		t.Errorf("after the eviction and a plain read = %+v, want tracked and not watched", got)
	}
}
