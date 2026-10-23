package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Docket pg2-12gr4.10: core chooses the handler path PER ROLE. A role with
// handler is on the new path; a role without it keeps today's path.

func TestHandlerCommandFor_roleWithHandlerResolvesItsOwnCommand(t *testing.T) {
	// No global handler command at all: the new path needs none.
	got, err := handlerCommandFor(config.Config{})(roles.Role{Name: "r", Handler: "/opt/h"}, "dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/h", "dispatch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

// With the global command AND the role-config directory set, a role with
// handler still gets [handler, subcommand] with no --role-config, while a role
// without handler gets exactly today's argv.
func TestHandlerCommandFor_choosesThePathPerRole(t *testing.T) {
	cfg := config.Config{HandlerCommand: "shared", HandlerCommandDir: "/dir"}
	commandFor := handlerCommandFor(cfg)
	for _, sub := range []string{"dispatch", "postStartup", "preShutdown", "validate"} {
		got, err := commandFor(roles.Role{Name: "new", Handler: "/opt/h"}, sub)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"/opt/h", sub}; !reflect.DeepEqual(got, want) {
			t.Errorf("new path argv = %v, want %v (no --role-config)", got, want)
		}
		got, err = commandFor(roles.Role{Name: "old"}, sub)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"shared", sub, "--role-config", "/dir/old.json"}; !reflect.DeepEqual(got, want) {
			t.Errorf("old path argv = %v, want %v", got, want)
		}
	}
}

// A role's survival is read from the TOML for a role with handler (even when
// the old per-role JSON says otherwise), and from the JSON for a role without.
func TestRoleSurvives_choosesThePathPerRole(t *testing.T) {
	dir := t.TempDir()
	writeRoleFile(t, dir, "old-yes", `{"survivesShutdown":true}`)
	writeRoleFile(t, dir, "new-no", `{"survivesShutdown":true}`) // ignored: the new path reads the TOML
	cfg := config.Config{HandlerCommandDir: dir}
	cases := []struct {
		role roles.Role
		want bool
	}{
		{roles.Role{Name: "old-yes"}, true},
		{roles.Role{Name: "old-no"}, false},
		{roles.Role{Name: "old-yes", SurvivesShutdown: false}, true},
		{roles.Role{Name: "new-yes", Handler: "/h", SurvivesShutdown: true}, true},
		{roles.Role{Name: "new-no", Handler: "/h", SurvivesShutdown: false}, false},
		// The TOML key alone does not opt a role without handler in.
		{roles.Role{Name: "old-no", SurvivesShutdown: true}, false},
	}
	for _, tc := range cases {
		if got := roleSurvives(cfg, tc.role); got != tc.want {
			t.Errorf("roleSurvives(%+v) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestSurvivingRoles_includesNewPathRolesFromTheTOML(t *testing.T) {
	cfg := config.Config{Roles: roles.RoleSet{
		{Name: "keep", Enabled: true, Handler: "/h", SurvivesShutdown: true},
		{Name: "drop", Enabled: true, Handler: "/h"},
		{Name: "off", Enabled: false, Handler: "/h", SurvivesShutdown: true},
	}}
	got := survivingRoles(cfg)
	if len(got) != 1 || !got["keep"] {
		t.Fatalf("survivingRoles = %v, want only {keep}", got)
	}
}

func permutations(rs []roles.Role) [][]roles.Role {
	if len(rs) <= 1 {
		return [][]roles.Role{append([]roles.Role(nil), rs...)}
	}
	var out [][]roles.Role
	for i := range rs {
		rest := append(append([]roles.Role(nil), rs[:i]...), rs[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]roles.Role{rs[i]}, p...))
		}
	}
	return out
}

// Mixed old-path and new-path roles, in every order: the old shared handler
// still receives preShutdown ONCE (pg2-asr8z) and postStartup per old role as
// today, and every distinct adapter named by an enabled role with handler
// receives preShutdown once and postStartup per role. Nothing is left out.
func TestLifecycleRouting_mixedOldAndNewPathInAnyOrder(t *testing.T) {
	base := []roles.Role{
		{Name: "old1", Enabled: true},
		{Name: "old2", Enabled: true},
		{Name: "newA1", Enabled: true, Handler: "/opt/a"},
		{Name: "newA2", Enabled: true, Handler: "/opt/a"},
		{Name: "newB", Enabled: true, Handler: "/opt/b"},
		{Name: "off", Enabled: false, Handler: "/opt/c"},
	}
	for _, order := range permutations(base) {
		fh := &fakeHandlerClient{}
		o := &orchestrator.Orchestrator{Handler: fh}
		cfg := config.Config{Roles: roles.RoleSet(order)}
		postStartupAll(context.Background(), o, cfg)
		preShutdownAll(context.Background(), o, cfg)

		// postStartup: every enabled role, in config order.
		var wantPost []string
		for _, r := range order {
			if r.Enabled {
				wantPost = append(wantPost, r.Name)
			}
		}
		if got := fh.postStartupCalls(); !reflect.DeepEqual(got, wantPost) {
			t.Fatalf("order %v: postStartup = %v, want %v", names(order), got, wantPost)
		}

		// preShutdown: first enabled old role once; first enabled role of each
		// distinct handler once; in config order of those carriers.
		var wantPre []string
		oldSeen, seen := false, map[string]bool{}
		for _, r := range order {
			switch {
			case !r.Enabled:
			case r.Handler == "" && !oldSeen:
				oldSeen = true
				wantPre = append(wantPre, r.Name)
			case r.Handler != "" && !seen[r.Handler]:
				seen[r.Handler] = true
				wantPre = append(wantPre, r.Name)
			}
		}
		if got := fh.preShutdownCalls(); !reflect.DeepEqual(got, wantPre) {
			t.Fatalf("order %v: preShutdown = %v, want %v", names(order), got, wantPre)
		}
		if len(wantPre) != 3 {
			t.Fatalf("test setup: want 3 carriers (old, /opt/a, /opt/b), got %v", wantPre)
		}
	}
}

func names(rs []roles.Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}
