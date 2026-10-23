package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/roles"
)

// roleHandlerCfg wraps role bodies in a valid [[query]] so Load succeeds: the
// tests below are about the three new [[role]] keys (handler, [role.config],
// survives_shutdown) only.
func roleHandlerCfg(roleBodies ...string) string {
	return `
[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

` + strings.Join(roleBodies, "\n")
}

func loadRoleCfg(t *testing.T, body string) (Config, error) {
	t.Helper()
	absentGlobalConfig(t)
	writeCfg(t, body)
	return Load()
}

func TestLoad_roleHandlerKeysDecode(t *testing.T) {
	c, err := loadRoleCfg(t, roleHandlerCfg(`
[[role]]
name = "review"
binds = ["e"]
handler = "/opt/adapters/pg-router-handler-ccpool"
survives_shutdown = true
[role.config]
actor = "reviewer"
[role.config.sessionEnv]
K = "v"
`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles[0]
	if r.Handler != "/opt/adapters/pg-router-handler-ccpool" || !r.NewPath() {
		t.Errorf("Handler = %q, want the absolute adapter path", r.Handler)
	}
	if !r.SurvivesShutdown {
		t.Error("SurvivesShutdown = false, want true")
	}
	want := map[string]any{"actor": "reviewer", "sessionEnv": map[string]any{"K": "v"}}
	if !reflect.DeepEqual(r.Config, want) {
		t.Errorf("Config = %#v, want %#v (opaque, nested tables kept)", r.Config, want)
	}
}

// A role with handler and no [role.config] / survives_shutdown gets an empty
// (non-nil) config and the default false.
func TestLoad_roleHandlerOnlyDefaults(t *testing.T) {
	c, err := loadRoleCfg(t, roleHandlerCfg(`
[[role]]
name = "r"
binds = ["e"]
handler = "/bin/h"
`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles[0]
	if r.Config == nil || len(r.Config) != 0 {
		t.Errorf("Config = %#v, want an empty non-nil map", r.Config)
	}
	if r.SurvivesShutdown {
		t.Error("SurvivesShutdown = true, want default false")
	}
}

func TestLoad_roleHandlerKeysRejectBadShapes(t *testing.T) {
	cases := []struct {
		name string
		role string
		want string
	}{
		{"handler wrong type", "handler = 5", "handler"},
		{"handler relative", `handler = "bin/h"`, "absolute path"},
		{"handler bare command name", `handler = "h"`, "absolute path"},
		{"handler empty", `handler = ""`, "absolute path"},
		{"config wrong type", "handler = \"/bin/h\"\nconfig = 5", "config"},
		{"config an array", "handler = \"/bin/h\"\nconfig = [1]", "config"},
		{"survives_shutdown wrong type", "handler = \"/bin/h\"\nsurvives_shutdown = \"yes\"", "survives_shutdown"},
		{"config without handler", "[role.config]\na = 1", "[role.config] is set but handler is not"},
		{"survives_shutdown without handler", "survives_shutdown = true", "survives_shutdown is set but handler is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRoleCfg(t, roleHandlerCfg("[[role]]\nname = \"r\"\nbinds = [\"e\"]\n"+tc.role+"\n"))
			if err == nil {
				t.Fatalf("load succeeded, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A role without handler decodes exactly as before: no new fields set, and
// other unknown [[role]] keys are still silently ignored.
func TestLoad_roleWithoutHandlerDecodesAsBefore(t *testing.T) {
	c, err := loadRoleCfg(t, roleHandlerCfg(`
[[role]]
name = "old"
binds = ["e"]
type = "command"
someFutureKey = "ignored"
[role.command]
argv = ["x"]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := c.Roles[0]
	if got.Handler != "" || got.Config != nil || got.SurvivesShutdown || got.NewPath() {
		t.Errorf("old-path role carries new-path fields: %+v", got)
	}
}

// With HandlerCommandDir set, only OLD-path roles need a per-role JSON file.
func TestRoleConfigFindings_skipsRolesWithOwnHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Config{HandlerCommandDir: dir, Roles: roles.RoleSet{
		{Name: "old", Enabled: true},
		{Name: "new", Enabled: true, Handler: "/bin/h"},
	}}
	if errs := c.roleConfigFindings(); len(errs) != 0 {
		t.Fatalf("findings = %v, want none (the new-path role has no JSON file by design)", errs)
	}
	c.Roles = append(c.Roles, roles.Role{Name: "old-missing", Enabled: true})
	if errs := c.roleConfigFindings(); len(errs) != 1 {
		t.Fatalf("findings = %v, want exactly the old-path role with no file", errs)
	}
}
