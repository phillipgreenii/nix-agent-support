package main

import (
	"fmt"
	"strings"
	"testing"
)

// --watch on pr show (INTF-WIRE-WATCH-1, INTF-WIRE-WATCH-2): a set flag is
// forwarded as watch: true in the show request to every backend tried, whether or
// not the backend keeps a store, the key is absent when the flag is unset, and a
// show that carries it always reaches the backend, never the umbrella's own cache
// alone.

func TestPrShowWatch_ForwardedOnlyWhenSetToAnyBackendKind(t *testing.T) {
	for _, optOut := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			cmd  []string
			want string
		}{
			{"unset", []string{"pr", "show", "pr-1"}, `{"id":"pr-1"}`},
			{"watch", []string{"pr", "show", "pr-1", "--watch"}, `{"id":"pr-1","watch":true}`},
			{"watch=false", []string{"pr", "show", "pr-1", "--watch=false"}, `{"id":"pr-1"}`},
		} {
			t.Run(fmt.Sprintf("optOut=%t/%s", optOut, tc.name), func(t *testing.T) {
				b := newFreshFakeBackend(t, "watch-single", optOut, false)
				writeFreshConfig(t, b)
				if stdout, code := freshRun(t, tc.cmd); code != 0 {
					t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
				}
				if got := b.argsOf(t, "show"); len(got) != 1 || got[0] != tc.want {
					t.Fatalf("show requests = %v, want exactly one with args %s", got, tc.want)
				}
			})
		}
	}
}

func TestPrShowWatch_CombinesWithFresh(t *testing.T) {
	b := newFreshFakeBackend(t, "watch-fresh", true, false)
	writeFreshConfig(t, b)
	if stdout, code := freshRun(t, []string{"pr", "show", "pr-1", "--watch", "--fresh"}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if got := b.argsOf(t, "show"); len(got) != 1 || got[0] != `{"fresh":true,"id":"pr-1","watch":true}` {
		t.Fatalf("show requests = %v, want one carrying fresh and watch", got)
	}
}

func TestPrShowWatch_EveryBackendTriedGetsTheOption(t *testing.T) {
	first := newFreshFakeBackend(t, "watch-try-first", true, true) // does not know the id
	second := newFreshFakeBackend(t, "watch-try-second", false, false)
	writeFreshConfig(t, first, second)
	if stdout, code := freshRun(t, []string{"pr", "show", "pr-1", "--watch"}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	for _, b := range []*freshFakeBackend{first, second} {
		if got := b.argsOf(t, "show"); len(got) != 1 || got[0] != `{"id":"pr-1","watch":true}` {
			t.Fatalf("%s show requests = %v, want one with watch: true", b.name, got)
		}
	}
}

func TestPrShowWatch_NeverAnsweredFromTheUmbrellaCacheAlone(t *testing.T) {
	b := newFreshFakeBackend(t, "watch-cache", false, false)
	writeFreshConfig(t, b)
	for i := 0; i < 2; i++ {
		if stdout, code := freshRun(t, []string{"pr", "show", "pr-1"}); code != 0 {
			t.Fatalf("show %d: exit code = %d; stdout=%s", i, code, stdout)
		}
	}
	if got := b.argsOf(t, "show"); len(got) != 1 {
		t.Fatalf("control: the second plain show must be a cache hit, backend saw %v", got)
	}
	if stdout, code := freshRun(t, []string{"pr", "show", "pr-1", "--watch"}); code != 0 {
		t.Fatalf("watch show: exit code = %d; stdout=%s", code, stdout)
	}
	got := b.argsOf(t, "show")
	if len(got) != 2 || got[1] != `{"id":"pr-1","watch":true}` {
		t.Fatalf("a show with --watch must reach the backend although the cache holds the entity, backend saw %v", got)
	}
}

func TestPrShowWatch_HelpListsTheFlagAndNoOtherShowGainedIt(t *testing.T) {
	stdout, _, code := executePr(t, []string{"pr", "show", "--help"})
	if code != 0 || !strings.Contains(stdout, "--watch") {
		t.Fatalf("pr show --help exit %d, does not list --watch:\n%s", code, stdout)
	}
	for _, other := range [][]string{{"pr", "files", "--help"}, {"pr", "commits", "--help"}, {"pr", "list", "--help"}, {"issue", "list", "--help"}, {"issue", "changes", "--help"}} {
		out, _, _ := executePr(t, other)
		if strings.Contains(out, "--watch") {
			t.Errorf("%v lists --watch; the option is on show only:\n%s", other, out)
		}
	}
}
