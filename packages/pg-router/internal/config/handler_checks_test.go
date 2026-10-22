package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// handlerWiring is a valid wiring (one source, one enabled role) to which a test
// adds a handler command and/or role-config directory.
func handlerWiring() Config {
	return wiring(
		query.SourceSet{eventSource("s", "a.ready")},
		roles.RoleSet{cmdRole("r", "a.ready")},
	)
}

func TestValidate_handlerCommandUnsetIsLegal(t *testing.T) {
	if errs, _ := handlerWiring().diagnose(); len(errs) != 0 {
		t.Fatalf("an unset handler command must not fail validation; got %v", errs)
	}
}

func TestValidate_absentHandlerCommandIsAnError(t *testing.T) {
	c := handlerWiring()
	c.HandlerCommand = "absent-handler"
	errs, _ := c.diagnose()
	if !findingsContain(errs, `handler command "absent-handler"`) || !findingsContain(errs, "absent backing command") {
		t.Fatalf("an uninvocable handler command must be named; got %v", errs)
	}
	c.Locator = stubLocator{present: map[string]bool{"absent-handler": true, "present-tool": true}}
	if errs, _ := c.diagnose(); len(errs) != 0 {
		t.Fatalf("an invocable handler command is valid; got %v", errs)
	}
}

func writeRoleConfig(t *testing.T, dir, role, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, role+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_roleConfigFile(t *testing.T) {
	dir := t.TempDir()
	c := handlerWiring()
	c.HandlerCommandDir = dir

	errs, _ := c.diagnose()
	if !findingsContain(errs, `role "r" role-config file`) || !findingsContain(errs, filepath.Join(dir, "r.json")) {
		t.Fatalf("a missing role-config file must be named; got %v", errs)
	}

	writeRoleConfig(t, dir, "r", `{ not json`)
	errs, _ = c.diagnose()
	if !findingsContain(errs, "not well-formed JSON") {
		t.Fatalf("a malformed role-config file must be reported; got %v", errs)
	}

	// Well-formed JSON is enough: core does not interpret the handler's fields.
	writeRoleConfig(t, dir, "r", `{"anything":["goes",1,null]}`)
	if errs, _ := c.diagnose(); len(errs) != 0 {
		t.Fatalf("a well-formed role-config file is valid; got %v", errs)
	}
}

func TestValidate_roleConfigSkipsDisabledRolesAndUnsetDir(t *testing.T) {
	c := handlerWiring()
	// No dir: nothing to check.
	if errs, _ := c.diagnose(); len(errs) != 0 {
		t.Fatalf("no dir, no role-config check; got %v", errs)
	}
	c.HandlerCommandDir = t.TempDir()
	c.Roles = roles.RoleSet{{Name: "r", Enabled: false, Binds: []string{"a.ready"}}}
	if errs, _ := c.diagnose(); len(errs) != 0 {
		t.Fatalf("a disabled role has no role-config check; got %v", errs)
	}
}

// The two new checks refuse Load, aggregated with the existing ones.
func TestLoad_handlerChecksRefuseStartAggregated(t *testing.T) {
	absentGlobalConfig(t)
	dir := t.TempDir()
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", "absent-handler")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND_DIR", dir)
	writeRoleConfig(t, dir, "r", `{`)
	writeCfg(t, `
[[query]]
name = "s"
emits = ["a.ready"]
type = "command"
[query.command]
argv = ["absent-lister"]
format = "jsonl"

[[role]]
name = "r"
binds = ["a.ready"]
`)
	_, err := Load()
	if err == nil {
		t.Fatal("Load must refuse a config whose handler command and role-config are bad")
	}
	for _, want := range []string{`handler command "absent-handler"`, "not well-formed JSON", `source "s" backing command "absent-lister"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load error %q does not contain %q (the checks aggregate)", err, want)
		}
	}
}
