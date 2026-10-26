package list

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The watched flag as entity_meta on every entity of a list answer (bead
// pg2-xxcdow): read from the stored flag of the PR's entity row, never a field
// of the summary content. The wire behavior over the real daemon is in
// internal/serve.

// setWatched flips the stored flag the way a watched query run does, bypassing
// every read path.
func (r *rig) setWatched(node string) {
	r.t.Helper()
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.SetWatched(context.Background(), entity.KindPR, node, registry.ViaQuery+"mine", r.clk.Now())
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func TestList_EveryEntityStatesTheStoredWatchedFlag(t *testing.T) {
	r := newRig(t)
	first, _ := r.list(`{"query":"mine"}`, "")
	if len(first.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(first.Entities))
	}
	for _, e := range first.Entities {
		if watched, stated := e.EntityMeta.WatchedState(); !stated || watched {
			t.Errorf("%s before any watch: entity_meta = %+v, want watched false stated", e.ID, e.EntityMeta)
		}
	}

	r.setWatched(first.Entities[0].NodeID)
	again, _ := r.list(`{"query":"mine"}`, "")
	for i, e := range again.Entities {
		want := e.NodeID == first.Entities[0].NodeID
		if watched, stated := e.EntityMeta.WatchedState(); !stated || watched != want {
			t.Errorf("entity %d (%s) after the flip: entity_meta = %+v, want watched %v stated", i, e.ID, e.EntityMeta, want)
		}
	}
	// Listing only reads the flag: the other PR is still not watched.
	if ent, found, err := r.st.GetEntity(context.Background(), entity.KindPR, first.Entities[1].NodeID); err != nil || !found || !ent.WatchedAt.IsZero() {
		t.Errorf("the unflipped PR = %+v found %v (%v), want a row that is not watched", ent, found, err)
	}
}

func TestList_TheFlagIsOutsideTheContentAndTheFingerprint(t *testing.T) {
	r := newRig(t)
	res, _ := r.list(`{"query":"mine"}`, "")
	if len(res.FingerprintExcludes) != 1 || res.FingerprintExcludes[0] != "entity_meta" {
		t.Errorf("fingerprint_excludes = %v, want [entity_meta]", res.FingerprintExcludes)
	}
	// ids_only has no entities, so nothing carries the flag and nothing is hashed.
	ids, _ := r.list(`{"query":"mine","ids_only":true}`, "")
	if len(ids.Entities) != 0 || len(ids.FingerprintExcludes) != 0 {
		t.Errorf("ids_only answer = %+v, want no entities and no excludes", ids)
	}
}
