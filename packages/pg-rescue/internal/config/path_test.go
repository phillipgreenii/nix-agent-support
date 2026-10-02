package config

import "testing"

func TestResolvePathLookupOrder(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }

	if p, src := ResolvePath("", get, "/h"); p != "/h/.config/pg-rescue/config.toml" || src != "~/.config" {
		t.Errorf("default = %q (%s)", p, src)
	}
	env["XDG_CONFIG_HOME"] = "/xdg"
	if p, src := ResolvePath("", get, "/h"); p != "/xdg/pg-rescue/config.toml" || src != "$XDG_CONFIG_HOME" {
		t.Errorf("xdg = %q (%s)", p, src)
	}
	env["PG_RESCUE_CONFIG"] = "/env.toml"
	if p, src := ResolvePath("", get, "/h"); p != "/env.toml" || src != "$PG_RESCUE_CONFIG" {
		t.Errorf("env = %q (%s)", p, src)
	}
	if p, src := ResolvePath("/flag.toml", get, "/h"); p != "/flag.toml" || src != "--config" {
		t.Errorf("flag = %q (%s)", p, src)
	}
}

func TestResolvePathTreatsEmptyAsUnset(t *testing.T) {
	get := func(k string) string { return "" }
	if p, _ := ResolvePath("", get, "/h"); p != "/h/.config/pg-rescue/config.toml" {
		t.Errorf("empty values should fall through, got %q", p)
	}
}
