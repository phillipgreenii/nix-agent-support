package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersed_flagsBeforeAfterBetweenPositionals(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantPos []string
		wantCwd string
		wantNW  bool
	}{
		{"flags-first", []string{"--cwd", "/p", "--no-wait", "alpha", "hello"}, []string{"alpha", "hello"}, "/p", true},
		{"value-flag-after-positional", []string{"alpha", "--cwd", "/p"}, []string{"alpha"}, "/p", false},
		{"bool-flag-after-positionals", []string{"alpha", "hello", "--no-wait"}, []string{"alpha", "hello"}, "", true},
		{"flag-between-positionals", []string{"alpha", "--cwd", "/p", "hello"}, []string{"alpha", "hello"}, "/p", false},
		{"no-flags", []string{"alpha", "hello"}, []string{"alpha", "hello"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			cwd := fs.String("cwd", "", "")
			nw := fs.Bool("no-wait", false, "")
			pos := parseInterspersed(fs, tc.args)
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
			if *cwd != tc.wantCwd {
				t.Errorf("--cwd = %q, want %q", *cwd, tc.wantCwd)
			}
			if *nw != tc.wantNW {
				t.Errorf("--no-wait = %v, want %v", *nw, tc.wantNW)
			}
		})
	}
}
