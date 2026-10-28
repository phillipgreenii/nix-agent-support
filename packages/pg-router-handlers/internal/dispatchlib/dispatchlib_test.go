package dispatchlib

import (
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
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

// buildRunner runs BuildDeps for role and returns the deps with the ccpool
// runner the dispatch will actually launch through.
func buildRunner(t *testing.T, cfg config.Config, role roles.Role) (executor.Deps, *ccpool.CLIRunner) {
	t.Helper()
	// BuildDeps opens <stateDir>/events.jsonl; keep it out of the developer's
	// real handler state directory.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	deps := BuildDeps(cfg, role)
	cc, ok := deps.CC.(*ccpool.CLIRunner)
	if !ok {
		t.Fatalf("deps.CC is %T, want *ccpool.CLIRunner", deps.CC)
	}
	return deps, cc
}

// INV-CCH-40: a role's model replaces the handler-wide model, and it does so
// BEFORE the runner is built: the runner's own Model is what `--model` reads, so
// asserting deps.Cfg alone would pass with a stale runner.
func TestBuildDeps_roleModelOverridesHandlerModel(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "claude-sonnet-5-5"
	role := roles.Role{Name: "triage", CCPool: &roles.CCPoolConfig{Actor: "a", Model: "claude-opus-5-5"}}
	deps, cc := buildRunner(t, cfg, role)
	if deps.Cfg.Model != "claude-opus-5-5" {
		t.Errorf("deps.Cfg.Model = %q, want claude-opus-5-5", deps.Cfg.Model)
	}
	if cc.Model != "claude-opus-5-5" {
		t.Errorf("runner Model = %q, want claude-opus-5-5", cc.Model)
	}
}

// INV-CCH-40: a role with no model keeps the handler-wide model.
func TestBuildDeps_noRoleModelKeepsHandlerModel(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "claude-sonnet-5-5"
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{Actor: "a"}}
	deps, cc := buildRunner(t, cfg, role)
	if deps.Cfg.Model != "claude-sonnet-5-5" || cc.Model != "claude-sonnet-5-5" {
		t.Errorf("deps.Cfg.Model = %q, runner Model = %q, want claude-sonnet-5-5 for both", deps.Cfg.Model, cc.Model)
	}
}

// INV-CCH-40, INV-CCH-31: each override applies alone: a model-only role leaves
// the handler effort, an effort-only role leaves the handler model.
func TestBuildDeps_modelAndEffortOverrideIndependently(t *testing.T) {
	cfg := config.Default()
	cfg.Model, cfg.Effort = "claude-sonnet-5-5", "medium"

	t.Run("model only", func(t *testing.T) {
		role := roles.Role{Name: "r", CCPool: &roles.CCPoolConfig{Actor: "a", Model: "claude-opus-5-5"}}
		_, cc := buildRunner(t, cfg, role)
		if cc.Model != "claude-opus-5-5" || cc.Effort != "medium" {
			t.Errorf("runner model/effort = %q/%q, want claude-opus-5-5/medium", cc.Model, cc.Effort)
		}
	})
	t.Run("effort only", func(t *testing.T) {
		role := roles.Role{Name: "r", CCPool: &roles.CCPoolConfig{Actor: "a", Effort: "xhigh"}}
		_, cc := buildRunner(t, cfg, role)
		if cc.Model != "claude-sonnet-5-5" || cc.Effort != "xhigh" {
			t.Errorf("runner model/effort = %q/%q, want claude-sonnet-5-5/xhigh", cc.Model, cc.Effort)
		}
	})
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
