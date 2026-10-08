package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
)

// TestCheckReload pins INV-CONF-12: a reload that removes the active profile is
// a bad reload, and one that removes any other profile is not. A configuration
// that fails Parse never reaches CheckReload, since there is no *Config for it.
func TestCheckReload(t *testing.T) {
	prev := mustParseValid(t)

	t.Run("removing the active profile fails", func(t *testing.T) {
		next := mustParse(t, edited(t, func(c map[string]any) {
			delete(at(c, "profiles"), "on-call")
			delete(at(c, "profiles"), "normal")
			at(c, "profiles")["other"] = map[string]any{}
			at(c, "defaults")["profile"] = "other"
		}))
		err := config.CheckReload(prev, next, "normal")
		ve := validationError(t, err)
		if len(ve.Problems) != 1 || ve.Problems[0].Path != "/profiles/normal" {
			t.Fatalf("problems = %+v, want one at /profiles/normal", ve.Problems)
		}
		for _, frag := range []string{`"normal"`, "active", "removes"} {
			if !strings.Contains(ve.Problems[0].Message, frag) {
				t.Errorf("message %q does not contain %q", ve.Problems[0].Message, frag)
			}
		}
	})

	// INV-CONF-15: removing a non-active profile is allowed too.
	t.Run("removing a profile that is not active passes", func(t *testing.T) {
		next := mustParse(t, edited(t, func(c map[string]any) { delete(at(c, "profiles"), "on-call") }))
		if err := config.CheckReload(prev, next, "normal"); err != nil {
			t.Errorf("CheckReload = %v, want nil", err)
		}
	})

	// INV-CONF-15: a reload that removes a task definition or a cycle type is
	// allowed, because history never consults the configuration.
	t.Run("removing tasks and cycle types the history mentions passes", func(t *testing.T) {
		next := mustParse(t, edited(t, func(c map[string]any) {
			delete(at(c, "tasks"), "post-plan")
			delete(at(c, "cycles"), "deep-work")
			for _, p := range []string{"normal", "on-call"} {
				at(c, "profiles", p)["daily"] = []any{"plan-day", "end-of-day-summary"}
			}
			at(c, "profiles", "normal")["cycles"] = []any{"review"}
		}))
		if err := config.CheckReload(prev, next, "normal"); err != nil {
			t.Errorf("CheckReload = %v, want nil", err)
		}
	})

	t.Run("the same configuration passes", func(t *testing.T) {
		if err := config.CheckReload(prev, mustParseValid(t), "on-call"); err != nil {
			t.Errorf("CheckReload = %v, want nil", err)
		}
	})

	t.Run("no active profile yet passes", func(t *testing.T) {
		if err := config.CheckReload(prev, prev, ""); err != nil {
			t.Errorf("CheckReload = %v, want nil", err)
		}
	})

	t.Run("a first load has no previous configuration", func(t *testing.T) {
		if err := config.CheckReload(nil, prev, "normal"); err != nil {
			t.Errorf("CheckReload = %v, want nil", err)
		}
	})

	t.Run("a missing next configuration fails", func(t *testing.T) {
		_ = validationError(t, config.CheckReload(prev, nil, "normal"))
	})
}

// TestRestartRequired pins INV-CONF-13: a change to listen_port or public_url
// takes effect only on restart and is reported; nothing else is.
func TestRestartRequired(t *testing.T) {
	prev := mustParseValid(t)
	tests := []struct {
		name string
		edit func(c map[string]any)
		want []string
	}{
		{"nothing changes", func(c map[string]any) {}, nil},
		{"the port changes", func(c map[string]any) { c["listen_port"] = 49211 }, []string{"listen_port"}},
		{"the public URL changes", func(c map[string]any) { c["public_url"] = "https://other.example.test" }, []string{"public_url"}},
		{"the public URL is removed", func(c map[string]any) { delete(c, "public_url") }, []string{"public_url"}},
		{"both change", func(c map[string]any) { c["listen_port"] = 1; c["public_url"] = "http://example.test" }, []string{"listen_port", "public_url"}},
		{"a sound changes", func(c map[string]any) { at(c, "defaults", "alert")["sound"] = "Ping" }, nil},
		{"a task is removed", func(c map[string]any) {
			delete(at(c, "tasks"), "post-plan")
			at(c, "profiles", "normal")["daily"] = []any{"plan-day"}
			at(c, "profiles", "on-call")["daily"] = []any{"plan-day"}
		}, nil},
		{"the default profile changes", func(c map[string]any) { at(c, "defaults")["profile"] = "on-call" }, nil},
		{"the skew changes", func(c map[string]any) { at(c, "defaults")["max_future_skew_seconds"] = 5 }, nil},
		{"the group order changes", func(c map[string]any) { c["group_order"] = []any{"End of day"} }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := mustParse(t, edited(t, tc.edit))
			if got := config.RestartRequired(prev, next); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RestartRequired = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("the first load has nothing to compare", func(t *testing.T) {
		if got := config.RestartRequired(nil, prev); got != nil {
			t.Errorf("RestartRequired(nil, next) = %v, want nil", got)
		}
	})
}
