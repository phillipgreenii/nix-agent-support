package dispatchlib

import (
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// pg2-lhi3b: the production bd runner for a role carries that role's actor, so
// the handler's own bd writes (unclaim, labels, comments) are attributed to the
// role identity rather than to git user.name.
func TestBuildDeps_bdRunnerCarriesRoleActor(t *testing.T) {
	// BuildDeps opens <stateDir>/events.jsonl; keep it out of the developer's
	// real handler state directory.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{Actor: "pgii-pool__worker"}}
	deps := BuildDeps(config.Default(), role)
	r, ok := deps.BD.(*beads.CLIRunner)
	if !ok {
		t.Fatalf("deps.BD is %T, want *beads.CLIRunner", deps.BD)
	}
	if r.Actor != "pgii-pool__worker" {
		t.Errorf("bd runner Actor = %q, want pgii-pool__worker", r.Actor)
	}
}

func TestRoleActor_commandRoleHasNone(t *testing.T) {
	if got := RoleActor(roles.Role{Name: "c"}); got != "" {
		t.Errorf("RoleActor(command role) = %q, want empty", got)
	}
}

func TestItemFromPayload_readsTheFlatFields(t *testing.T) {
	it := ItemFromPayload(map[string]any{
		"id": "b-1", "type": "task", "title": "t", "metadata": map[string]any{"k": "v"},
	})
	if it.ID != "b-1" || it.Type != "task" || it.Title != "t" || it.Metadata["k"] != "v" {
		t.Errorf("ItemFromPayload = %+v", it)
	}
	if zero := ItemFromPayload(nil); zero.ID != "" || zero.Metadata != nil {
		t.Errorf("ItemFromPayload(nil) = %+v, want zero", zero)
	}
}

func TestBareEntityItem_mapsASuffixedIDBackToTheEntity(t *testing.T) {
	in := ItemFromPayload(map[string]any{
		"id": "pg2-9et@46d60fca149d", "type": "issue", "title": "t",
		"metadata": map[string]any{"entity_id": "pg2-9et", "k": "v"},
	})
	got := BareEntityItem(in)
	if got.ID != "pg2-9et" {
		t.Errorf("ID = %q, want the bare entity id pg2-9et", got.ID)
	}
	if got.Type != "issue" || got.Title != "t" || got.Metadata["k"] != "v" || got.Metadata["entity_id"] != "pg2-9et" {
		t.Errorf("every other field must be preserved: %+v", got)
	}
}

func TestBareEntityItem_leavesOtherItemsUnchanged(t *testing.T) {
	cases := map[string]item.Item{
		"no metadata":     {ID: "b-1"},
		"no entity_id":    {ID: "b-1@abc", Metadata: map[string]any{"k": "v"}},
		"empty entity_id": {ID: "b-1@abc", Metadata: map[string]any{"entity_id": ""}},
		"non-string":      {ID: "b-1@abc", Metadata: map[string]any{"entity_id": 7.0}},
		"bare id, same":   {ID: "b-1", Metadata: map[string]any{"entity_id": "b-1"}},
		"zero item":       {},
		"nil entity_id":   {ID: "b-1@abc", Metadata: map[string]any{"entity_id": nil}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := BareEntityItem(in); got.ID != in.ID {
				t.Errorf("ID = %q, want %q unchanged", got.ID, in.ID)
			}
		})
	}
}
