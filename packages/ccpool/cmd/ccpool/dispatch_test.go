package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// wantSubcommandNames is the exact 17-entry set pg2-htmkq's bug report named
// as "the real subcommand list": attach, attend, cancel, close, doctor,
// hook, list, meta, new, reap, reap-all, reply, result, state, tail, trust,
// version. Any drift from this set (an added/removed/renamed subcommand that
// forgot to update the registry) fails loudly here rather than silently
// shipping a help listing that doesn't match reality.
var wantSubcommandNames = []string{
	"attach", "attend", "cancel", "close", "doctor", "hook", "list", "meta",
	"new", "reap", "reap-all", "reply", "result", "state", "tail", "trust",
	"version",
}

func TestSubcommandRegistry_MatchesKnownSet(t *testing.T) {
	var got []string
	for _, s := range subcommands {
		got = append(got, s.name)
	}
	sort.Strings(got)
	want := append([]string{}, wantSubcommandNames...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry names = %v, want %v", got, want)
	}
}

func TestSubcommandRegistry_EveryEntryHasADescriptionAndRunFunc(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range subcommands {
		if seen[s.name] {
			t.Errorf("duplicate subcommand name %q in registry", s.name)
		}
		seen[s.name] = true
		if strings.TrimSpace(s.desc) == "" {
			t.Errorf("subcommand %q has an empty description", s.name)
		}
		if s.run == nil {
			t.Errorf("subcommand %q has a nil run func", s.name)
		}
	}
}

func TestLookupSubcommand(t *testing.T) {
	for _, name := range wantSubcommandNames {
		s, ok := lookupSubcommand(name)
		if !ok {
			t.Errorf("lookupSubcommand(%q) = not found, want found", name)
			continue
		}
		if s.name != name {
			t.Errorf("lookupSubcommand(%q).name = %q", name, s.name)
		}
	}
	for _, bad := range []string{"send", "", "attach ", "Attach", "-help"} {
		if _, ok := lookupSubcommand(bad); ok {
			t.Errorf("lookupSubcommand(%q) = found, want not found", bad)
		}
	}
}

// TestPickSubcommand_NoneOfTheThreeFallThroughToList is the regression test
// for pg2-htmkq's root cause: bare invocation, every help spelling, and any
// unrecognized first argument must NOT be classified as dispatchKnown{list}
// -- they get their own dispatchKind so run() can print top-level usage
// instead of silently running `list`.
func TestPickSubcommand_NoneOfTheThreeFallThroughToList(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantKind dispatchKind
		wantSub  string // only checked when wantKind == dispatchKnown or dispatchUnknown
		wantRest []string
	}{
		{"no args at all", []string{"ccpool"}, dispatchHelp, "", nil},
		{"-h", []string{"ccpool", "-h"}, dispatchHelp, "", nil},
		{"--help", []string{"ccpool", "--help"}, dispatchHelp, "", nil},
		{"help", []string{"ccpool", "help"}, dispatchHelp, "", nil},
		{"help with trailing args", []string{"ccpool", "help", "list"}, dispatchHelp, "", []string{"list"}},
		{"typo", []string{"ccpool", "sned"}, dispatchUnknown, "sned", nil},
		{"unimplemented verb from the bug report", []string{"ccpool", "send", "alpha", "hi"}, dispatchUnknown, "send", []string{"alpha", "hi"}},
		{"known: list explicit", []string{"ccpool", "list"}, dispatchKnown, "list", nil},
		{"known: reply with args", []string{"ccpool", "reply", "alpha", "go ahead", "--no-wait"}, dispatchKnown, "reply", []string{"alpha", "go ahead", "--no-wait"}},
		{"known: attach", []string{"ccpool", "attach", "alpha"}, dispatchKnown, "attach", []string{"alpha"}},
		{"known: reap-all", []string{"ccpool", "reap-all"}, dispatchKnown, "reap-all", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, sub, rest := pickSubcommand(tc.argv)
			if kind != tc.wantKind {
				t.Fatalf("kind = %v, want %v", kind, tc.wantKind)
			}
			if kind != dispatchHelp && sub.name != tc.wantSub {
				t.Errorf("sub.name = %q, want %q", sub.name, tc.wantSub)
			}
			if kind == dispatchKnown && sub.run == nil {
				t.Errorf("dispatchKnown sub %q has a nil run func", sub.name)
			}
			// Normalize nil vs. empty-but-non-nil (argv[len(argv):] slicing
			// yields the latter): only the CONTENT of rest matters here.
			gotRest, wantRest := rest, tc.wantRest
			if len(gotRest) == 0 {
				gotRest = nil
			}
			if len(wantRest) == 0 {
				wantRest = nil
			}
			if !reflect.DeepEqual(gotRest, wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

func TestUsageText_ListsEverySubcommandWithADescription(t *testing.T) {
	out := usageText()
	if !strings.HasPrefix(out, "usage: ccpool ") {
		t.Errorf("usageText() does not open with a top-level usage line: %q", out)
	}
	for _, s := range subcommands {
		if !strings.Contains(out, s.name) {
			t.Errorf("usageText() missing subcommand name %q:\n%s", s.name, out)
		}
		if !strings.Contains(out, s.desc) {
			t.Errorf("usageText() missing description for %q:\n%s", s.name, out)
		}
	}
	// The concrete discoverability failure pg2-htmkq reproduced: `reply` (how
	// to answer a needs_input session) must be visible in the listing.
	if !strings.Contains(out, "reply") {
		t.Error("usageText() does not mention reply")
	}
	// Must NOT look like list's own flag.FlagSet usage (the pre-fix accident
	// this bug reported: `ccpool --help` printed "Usage of list:").
	if strings.Contains(out, "Usage of list") {
		t.Error("usageText() looks like list's own flag.FlagSet usage, not top-level help")
	}
}
