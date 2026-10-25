package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Docket pg2-12gr4.10: a role is invoked only through its own handler.

func TestHandlerCommandFor_roleWithHandlerResolvesItsOwnCommand(t *testing.T) {
	// No global handler command at all: a role's own handler needs none.
	got, err := handlerCommandFor(config.Config{})(roles.Role{Name: "r", Handler: "/opt/h"}, "dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/h", "dispatch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

// Every subcommand resolves to [handler, subcommand]: the role's settings ride
// on stdin, so no flag follows the subcommand.
func TestHandlerCommandFor_everySubcommandIsHandlerThenSubcommand(t *testing.T) {
	commandFor := handlerCommandFor(config.Config{})
	for _, sub := range []string{"dispatch", "postStartup", "preShutdown", "validate"} {
		got, err := commandFor(roles.Role{Name: "r", Handler: "/opt/h"}, sub)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"/opt/h", sub}; !reflect.DeepEqual(got, want) {
			t.Errorf("argv = %v, want %v", got, want)
		}
	}
}

// A role's survival is the generic survives_shutdown key core decoded from the
// TOML, nothing else.
func TestRoleSurvives_readsTheRolesOwnKey(t *testing.T) {
	cases := []struct {
		role roles.Role
		want bool
	}{
		{roles.Role{Name: "yes", Handler: "/h", SurvivesShutdown: true}, true},
		{roles.Role{Name: "no", Handler: "/h", SurvivesShutdown: false}, false},
		{roles.Role{Name: "absent", Handler: "/h"}, false},
	}
	for _, tc := range cases {
		if got := roleSurvives(tc.role); got != tc.want {
			t.Errorf("roleSurvives(%+v) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// survivingRoles considers only ENABLED roles: a disabled role registers no
// listener, so it can never have a dispatch to leave running.
func TestSurvivingRoles_onlyEnabledRolesWithTheKey(t *testing.T) {
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

// Roles of several adapters, in every order: every distinct adapter named by an
// enabled role receives preShutdown once (pg2-asr8z), carried by the first
// enabled role naming it, and postStartup is sent per enabled role. A disabled
// role's adapter is left out; nothing else is.
func TestLifecycleRouting_severalAdaptersInAnyOrder(t *testing.T) {
	base := []roles.Role{
		{Name: "a1", Enabled: true, Handler: "/opt/a"},
		{Name: "a2", Enabled: true, Handler: "/opt/a"},
		{Name: "b1", Enabled: true, Handler: "/opt/b"},
		{Name: "b2", Enabled: true, Handler: "/opt/b"},
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

		// preShutdown: first enabled role of each distinct handler once, in
		// config order of those carriers.
		var wantPre []string
		seen := map[string]bool{}
		for _, r := range order {
			if r.Enabled && !seen[r.Handler] {
				seen[r.Handler] = true
				wantPre = append(wantPre, r.Name)
			}
		}
		if got := fh.preShutdownCalls(); !reflect.DeepEqual(got, wantPre) {
			t.Fatalf("order %v: preShutdown = %v, want %v", names(order), got, wantPre)
		}
		if len(wantPre) != 2 {
			t.Fatalf("test setup: want 2 carriers (/opt/a, /opt/b), got %v", wantPre)
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
