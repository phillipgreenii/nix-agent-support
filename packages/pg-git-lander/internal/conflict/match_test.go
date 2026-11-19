package conflict_test

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/conflict"
)

func TestMatchRulesFirstMatchLayerOrderAndDefault(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Rules: []config.Rule{
			{ID: "xdg-repo:1:*.lock", Glob: "*.lock"},
			{ID: "repo-file:1:*", Glob: "*"},
			{ID: "xdg-defaults:1:**", Glob: "**"},
		},
		ConflictDefault: &config.Rule{ID: "default"},
	}
	ms, none := conflict.MatchRules(cfg, []string{"b.lock", "a.lock", "z.txt", "d/e.txt"})
	if len(none) != 0 {
		t.Fatalf("none = %v", none)
	}
	got := map[string][]string{}
	var order []string
	for _, m := range ms {
		got[m.Rule.ID] = m.Paths
		order = append(order, m.Rule.ID)
	}
	want := map[string][]string{
		"xdg-repo:1:*.lock": {"a.lock", "b.lock"},
		"repo-file:1:*":     {"z.txt"},
		"xdg-defaults:1:**": {"d/e.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if !reflect.DeepEqual(order, []string{"xdg-repo:1:*.lock", "repo-file:1:*", "xdg-defaults:1:**"}) {
		t.Fatalf("order %v", order)
	}

	cfg.Rules = cfg.Rules[:1]
	ms, none = conflict.MatchRules(cfg, []string{"a.lock", "q.txt"})
	if len(none) != 0 || len(ms) != 2 || ms[1].Rule.ID != "default" || ms[1].Paths[0] != "q.txt" {
		t.Fatalf("default not used last: %+v %v", ms, none)
	}

	cfg.ConflictDefault = nil
	_, none = conflict.MatchRules(cfg, []string{"a.lock", "q.txt"})
	if !reflect.DeepEqual(none, []string{"q.txt"}) {
		t.Fatalf("none = %v", none)
	}
}

func TestHasMarkers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"block", "a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> branch\nb\n", true},
		{"markdown underline", "Title\n=======\ntext\n", false},
		{"open only", "<<<<<<< HEAD\nx\n", false},
		{"close only", ">>>>>>> b\n", false},
		{"close before open", ">>>>>>> b\n<<<<<<< HEAD\n", false},
		{"no space", "<<<<<<<\n>>>>>>>\n", false},
		{"indented", "  <<<<<<< x\n  >>>>>>> y\n", false},
	}
	for _, c := range cases {
		if got := conflict.HasMarkers([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
