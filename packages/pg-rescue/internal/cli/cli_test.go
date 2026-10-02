package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseWrapperValid(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Options
	}{
		{
			"chain with command",
			[]string{"--chain", "sync", "--", "git", "pull", "--rebase"},
			Options{Chain: "sync", Argv: []string{"git", "pull", "--rebase"}},
		},
		{
			"handlers list keeps repeats",
			[]string{"--handlers", "a,b,a", "--", "true"},
			Options{Handlers: []string{"a", "b", "a"}, Argv: []string{"true"}},
		},
		{
			"all options",
			[]string{
				"--chain", "c", "-C", "/repo", "--context", "why", "--verify", "test -f x",
				"--result-file", "/tmp/r.json", "-vv", "--config", "/c.toml", "--", "cmd", "-x",
			},
			Options{
				Chain: "c", Dir: "/repo", Context: "why", Verify: "test -f x", HasVerify: true,
				ResultFile: "/tmp/r.json", Verbosity: VeryVerbose, ConfigPath: "/c.toml", Argv: []string{"cmd", "-x"},
			},
		},
		{
			"quiet",
			[]string{"-q", "--chain", "c", "--", "x"},
			Options{Chain: "c", Verbosity: Quiet, Argv: []string{"x"}},
		},
		{
			"single v",
			[]string{"-v", "--chain", "c", "--", "x"},
			Options{Chain: "c", Verbosity: Verbose, Argv: []string{"x"}},
		},
		{
			"separate -v -v",
			[]string{"-v", "-v", "--chain", "c", "--", "x"},
			Options{Chain: "c", Verbosity: VeryVerbose, Argv: []string{"x"}},
		},
		{
			"command flags are not ours",
			[]string{"--chain", "c", "--", "ls", "-C", "dir", "--chain", "z"},
			Options{Chain: "c", Argv: []string{"ls", "-C", "dir", "--chain", "z"}},
		},
		{
			"empty verify is still given",
			[]string{"--chain", "c", "--verify", "", "--", "x"},
			Options{Chain: "c", HasVerify: true, Argv: []string{"x"}},
		},
		{
			"stdin mode",
			[]string{"--stdin", "--handlers", "h", "--verify", "true"},
			Options{Handlers: []string{"h"}, Stdin: true, Verify: "true", HasVerify: true},
		},
		{
			"argv is not interpreted by a shell",
			[]string{"--chain", "c", "--", "echo", "$HOME;", "x"},
			Options{Chain: "c", Argv: []string{"echo", "$HOME;", "x"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseWrapper(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", *got, tc.want)
			}
		})
	}
}

func TestParseWrapperErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no selector", []string{"--", "x"}, "exactly one of --handlers or --chain"},
		{"both selectors", []string{"--handlers", "a", "--chain", "b", "--", "x"}, "mutually exclusive"},
		{"empty handlers", []string{"--handlers", "", "--", "x"}, "the list is empty"},
		{"empty handler entry", []string{"--handlers", "a,,b", "--", "x"}, "empty handler name at position 2"},
		{"empty chain", []string{"--chain", "", "--", "x"}, "chain name is empty"},
		{"stdin with command", []string{"--stdin", "--chain", "c", "--verify", "true", "--", "x"}, "--stdin cannot be combined"},
		{"stdin without verify", []string{"--stdin", "--chain", "c"}, "--stdin requires --verify"},
		{"no command", []string{"--chain", "c"}, "no command"},
		{"dash but empty command", []string{"--chain", "c", "--"}, "no command"},
		{"command without dash", []string{"--chain", "c", "git", "pull"}, "must follow --"},
		{"stray arg before dash", []string{"--chain", "c", "oops", "--", "x"}, "unexpected argument"},
		{"unknown flag", []string{"--chain", "c", "--bogus", "--", "x"}, "unknown flag"},
		{"quiet and verbose", []string{"-q", "-v", "--chain", "c", "--", "x"}, "-q cannot be combined with -v"},
		{"too verbose", []string{"-vvv", "--chain", "c", "--", "x"}, "at most twice"},
		{"missing flag value", []string{"--chain"}, "needs an argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWrapper(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want one containing %q", err, tc.want)
			}
		})
	}
}

func TestParseWrapperHelp(t *testing.T) {
	for _, a := range []string{"-h", "--help"} {
		if _, err := ParseWrapper([]string{a}); !errors.Is(err, ErrHelp) {
			t.Errorf("%s: err = %v; want ErrHelp", a, err)
		}
	}
}

func TestParseResult(t *testing.T) {
	got, err := ParseResult([]string{"resolved", "relocked", "--details-file", "d", "--meta-file", "m"})
	if err != nil {
		t.Fatal(err)
	}
	want := ResultOptions{Outcome: "resolved", Summary: "relocked", DetailsFile: "d", MetaFile: "m"}
	if *got != want {
		t.Errorf("got %+v want %+v", *got, want)
	}
	if got, err := ParseResult([]string{"declined"}); err != nil || got.Summary != "" || got.Outcome != "declined" {
		t.Errorf("bare outcome: %+v, %v", got, err)
	}
	for name, args := range map[string][]string{
		"no outcome": {}, "too many": {"resolved", "a", "b"}, "unknown flag": {"resolved", "--nope"},
	} {
		if _, err := ParseResult(args); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseCheck(t *testing.T) {
	got, err := ParseCheck([]string{"--chain", "sync", "--config", "/c"})
	if err != nil || *got != (CheckOptions{Chain: "sync", ConfigPath: "/c"}) {
		t.Errorf("got %+v, %v", got, err)
	}
	for name, args := range map[string][]string{
		"positional": {"sync"}, "empty chain": {"--chain", ""}, "unknown flag": {"--x"},
	} {
		if _, err := ParseCheck(args); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
