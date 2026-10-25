package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// handlerWiring is a valid wiring (one source, one enabled role) to which a test
// gives a role a handler path.
func handlerWiring() Config {
	return wiring(
		query.SourceSet{eventSource("s", "a.ready")},
		roles.RoleSet{cmdRole("r", "a.ready")},
	)
}

// Every enabled role has its handler path checked in the same run as the other
// findings, so a missing handler shows in one config check run; a disabled role
// is not started, so it is not checked.
func TestValidate_roleHandlerPathIsChecked(t *testing.T) {
	c := handlerWiring()
	c.Roles = roles.RoleSet{{Name: "r", Enabled: true, Binds: []string{"a.ready"}, Handler: "/abs/absent-adapter"}}
	errs, _ := c.diagnose()
	if !findingsContain(errs, `role "r" handler "/abs/absent-adapter"`) || !findingsContain(errs, "absent backing command") {
		t.Fatalf("a missing role handler must be named; got %v", errs)
	}
	c.Locator = stubLocator{present: map[string]bool{"/abs/absent-adapter": true, "present-tool": true}}
	if errs, _ := c.diagnose(); len(errs) != 0 {
		t.Fatalf("an invocable role handler is valid; got %v", errs)
	}
	c.Locator = stubLocator{present: map[string]bool{}}
	c.Roles[0].Enabled = false
	if errs, _ := c.diagnose(); findingsContain(errs, "absent-adapter") {
		t.Fatalf("a disabled role is not started, so its handler is not checked; got %v", errs)
	}
}

// An enabled role built in code without a handler is a finding naming the role:
// there is no shared fallback command to run it with.
func TestValidate_enabledRoleWithoutHandlerIsAnError(t *testing.T) {
	c := handlerWiring()
	c.Roles = roles.RoleSet{{Name: "bare", Enabled: true, Binds: []string{"a.ready"}}}
	errs, _ := c.diagnose()
	if !findingsContain(errs, `role "bare" has no handler`) {
		t.Fatalf("a role without handler must be named; got %v", errs)
	}
}

// RoleCommand is the one resolver: the role's own handler and the subcommand,
// nothing else; a role without handler is an error naming the role.
func TestRoleCommand_isTheRolesOwnHandler(t *testing.T) {
	got, err := Config{}.RoleCommand(roles.Role{Name: "n", Handler: "/h"}, "dispatch")
	if err != nil || !reflect.DeepEqual(got, []string{"/h", "dispatch"}) {
		t.Errorf("RoleCommand = %v, %v; want [/h dispatch]", got, err)
	}
	got, err = Config{}.RoleCommand(roles.Role{Name: "bare"}, "dispatch")
	if err == nil || got != nil || !strings.Contains(err.Error(), `role "bare" has no handler`) {
		t.Errorf("RoleCommand without handler = %v, %v; want an error naming the role", got, err)
	}
}
