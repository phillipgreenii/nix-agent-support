package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

func writeRoleFile(t *testing.T, dir, role, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, role+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRoleSurvivesShutdown(t *testing.T) {
	dir := t.TempDir()
	writeRoleFile(t, dir, "yes", `{"name":"yes","type":"ccpool","survivesShutdown":true,"ccpool":{"actor":"a"}}`)
	writeRoleFile(t, dir, "no", `{"name":"no","type":"command","command":{"argv":["x"]}}`)
	writeRoleFile(t, dir, "explicit-false", `{"survivesShutdown":false}`)
	writeRoleFile(t, dir, "garbage", `{`)
	writeRoleFile(t, dir, "wrongtype", `{"survivesShutdown":1}`)

	cases := []struct {
		name string
		cfg  config.Config
		role string
		want bool
	}{
		{"flag true", config.Config{HandlerCommandDir: dir}, "yes", true},
		{"key absent", config.Config{HandlerCommandDir: dir}, "no", false},
		{"explicit false", config.Config{HandlerCommandDir: dir}, "explicit-false", false},
		{"undecodable file fails safe to cancel", config.Config{HandlerCommandDir: dir}, "garbage", false},
		{"wrong json type fails safe to cancel", config.Config{HandlerCommandDir: dir}, "wrongtype", false},
		{"no file for the role", config.Config{HandlerCommandDir: dir}, "missing", false},
		{"no handler command dir at all", config.Config{}, "yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleSurvivesShutdown(tc.cfg, tc.role); got != tc.want {
				t.Fatalf("roleSurvivesShutdown(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

// survivingRoles considers only ENABLED roles: a disabled role registers no
// listener, so it can never have a dispatch to leave running.
func TestSurvivingRoles_onlyEnabled(t *testing.T) {
	dir := t.TempDir()
	writeRoleFile(t, dir, "on", `{"survivesShutdown":true}`)
	writeRoleFile(t, dir, "off", `{"survivesShutdown":true}`)
	writeRoleFile(t, dir, "cmd", `{"type":"command"}`)
	cfg := config.Config{HandlerCommandDir: dir, Roles: roles.RoleSet{
		{Name: "on", Enabled: true},
		{Name: "off", Enabled: false},
		{Name: "cmd", Enabled: true},
	}}
	got := survivingRoles(cfg)
	if len(got) != 1 || !got["on"] {
		t.Fatalf("survivingRoles = %v, want only {on}", got)
	}
}

// handlerCommandFor and the survival lookup MUST read the same per-role file:
// the flag is only meaningful if it sits in the file the handler is launched
// with.
func TestHandlerRoleConfigPath_isTheFileHandlerCommandForPassesTheHandler(t *testing.T) {
	cfg := config.Config{HandlerCommand: "h", HandlerCommandDir: "/dir"}
	argv, err := handlerCommandFor(cfg)(roles.Role{Name: "review"}, "dispatch")
	if err != nil {
		t.Fatal(err)
	}
	want := handlerRoleConfigPath("/dir", "review")
	if len(argv) != 4 || argv[2] != "--role-config" || argv[3] != want {
		t.Fatalf("argv = %v, want --role-config %q", argv, want)
	}
}
