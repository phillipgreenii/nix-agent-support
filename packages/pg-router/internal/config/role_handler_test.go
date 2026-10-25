package config

import (
	"reflect"
	"strings"
	"testing"
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
	if r.Handler != "/opt/adapters/pg-router-handler-ccpool" {
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
		{"no handler", "", "handler is required"},
		{"config without handler", "[role.config]\na = 1", "handler is required"},
		{"survives_shutdown without handler", "survives_shutdown = true", "handler is required"},
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

// A role without handler is a load error naming the role (the decision of
// packet pg2-12gr4.10.17: there is no shared fallback command, so a role with no
// handler of its own could never be invoked). The role still reports its name
// when other unknown [[role]] keys are present, which stay ignored.
func TestLoad_roleWithoutHandlerIsALoadErrorNamingTheRole(t *testing.T) {
	_, err := loadRoleCfg(t, roleHandlerCfg(`
[[role]]
name = "old"
binds = ["e"]
type = "command"
someFutureKey = "ignored"
[role.command]
argv = ["x"]
`))
	if err == nil {
		t.Fatal("a [[role]] without handler must fail the load")
	}
	for _, want := range []string{`"old"`, "handler is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}
