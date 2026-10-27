package main

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// --watch on issue show (INTF-WIRE-WATCH-1, INTF-WIRE-WATCH-2): a set flag is
// forwarded as watch: true in the show request, to a backend that declares
// cache_opt_out and to one that does not, the key is absent when the flag is
// unset, a show that carries it always reaches the backend and never the
// umbrella's own cache alone, and a backend that keeps no store (the fake
// ignores the key) answers the ordinary show with exit 0. issue list and issue
// changes have no such option. The pr counterpart is pr_show_watch_test.go.

func TestIssueShowWatch_ForwardedOnlyWhenSetToAnyBackendKind(t *testing.T) {
	for _, optOut := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			cmd  []string
			want string
		}{
			{"unset", []string{"issue", "show", "iss-1"}, `{"id":"iss-1"}`},
			{"watch", []string{"issue", "show", "iss-1", "--watch"}, `{"id":"iss-1","watch":true}`},
			{"watch=false", []string{"issue", "show", "iss-1", "--watch=false"}, `{"id":"iss-1"}`},
			{"watch+fresh", []string{"issue", "show", "iss-1", "--watch", "--fresh"}, ""},
		} {
			t.Run(fmt.Sprintf("optOut=%t/%s", optOut, tc.name), func(t *testing.T) {
				b := newFreshFakeBackend(t, "issue-watch-single", optOut, false)
				writeFreshConfig(t, b)
				if stdout, code := freshRun(t, tc.cmd); code != 0 {
					t.Fatalf("exit code = %d, want 0 (a backend that ignores watch answers the ordinary show); stdout=%s", code, stdout)
				}
				want := tc.want
				if tc.name == "watch+fresh" {
					// --fresh is forwarded to a cache_opt_out backend only.
					want = `{"id":"iss-1","watch":true}`
					if optOut {
						want = `{"fresh":true,"id":"iss-1","watch":true}`
					}
				}
				if got := b.argsOf(t, "show"); len(got) != 1 || got[0] != want {
					t.Fatalf("show requests = %v, want exactly one with args %s", got, want)
				}
			})
		}
	}
}

func TestIssueShowWatch_NeverAnsweredFromTheUmbrellaCacheAlone(t *testing.T) {
	b := newFreshFakeBackend(t, "issue-watch-cache", false, false)
	writeFreshConfig(t, b)
	for i := 0; i < 2; i++ {
		if stdout, code := freshRun(t, []string{"issue", "show", "pr-1"}); code != 0 {
			t.Fatalf("show %d: exit code = %d; stdout=%s", i, code, stdout)
		}
	}
	// The fake answers every show with the entity "pr-1", and the cache is keyed
	// by what the backend returned, so the id asked for is "pr-1"; the control
	// proves a plain show is a cache hit before the watch show is judged.
	if got := b.argsOf(t, "show"); len(got) != 1 {
		t.Fatalf("control: the second plain show must be a cache hit, backend saw %v", got)
	}
	if stdout, code := freshRun(t, []string{"issue", "show", "pr-1", "--watch"}); code != 0 {
		t.Fatalf("watch show: exit code = %d; stdout=%s", code, stdout)
	}
	got := b.argsOf(t, "show")
	if len(got) != 2 || got[1] != `{"id":"pr-1","watch":true}` {
		t.Fatalf("a show with --watch must reach the backend although the cache holds the entity, backend saw %v", got)
	}
}

func TestIssueShowWatch_HelpListsTheFlagAndNoOtherIssueVerbGainedIt(t *testing.T) {
	stdout, _, code := executePr(t, []string{"issue", "show", "--help"})
	if code != 0 || !strings.Contains(stdout, "--watch") {
		t.Fatalf("issue show --help exit %d, does not list --watch:\n%s", code, stdout)
	}
	for _, other := range [][]string{{"issue", "list", "--help"}, {"issue", "changes", "--help"}} {
		out, _, _ := executePr(t, other)
		if strings.Contains(out, "--watch") {
			t.Errorf("%v lists --watch; the option is on show only:\n%s", other, out)
		}
	}
}

func TestIssueShowWatch_ListAndChangesRejectTheFlag(t *testing.T) {
	b := newFreshFakeBackend(t, "issue-watch-reject", false, false)
	writeFreshConfig(t, b)
	for _, cmd := range [][]string{
		{"issue", "list", "--query", "mine", "--watch"},
		{"issue", "changes", "--consumer", "c", "--watch"},
	} {
		root := newRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(cmd)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag: --watch") {
			t.Errorf("%v: error = %v, want an unknown flag: --watch failure", cmd, err)
		}
	}
	for _, op := range []string{"list", "changes"} {
		if got := b.argsOf(t, op); len(got) != 0 {
			t.Errorf("a rejected %s still reached the backend: %v", op, got)
		}
	}
}
