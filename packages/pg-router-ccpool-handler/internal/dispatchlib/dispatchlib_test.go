package dispatchlib

import (
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// pg2-lhi3b: the production bd runner for a role carries that role's actor, so
// the handler's own bd writes (unclaim, labels, comments) are attributed to the
// role identity rather than to git user.name.
func TestBuildDeps_bdRunnerCarriesRoleActor(t *testing.T) {
	// BuildDeps opens <stateDir>/events.jsonl; keep it out of the developer's
	// real handler state directory.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	role := roles.Role{Name: "worker", Type: "ccpool", CCPool: &roles.CCPoolConfig{Actor: "pgii-pool__worker"}}
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
	if got := RoleActor(roles.Role{Name: "c", Type: "command"}); got != "" {
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
