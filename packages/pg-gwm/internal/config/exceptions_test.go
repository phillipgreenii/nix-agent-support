package config

import "testing"

func TestExceptionMatches(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/w/pristine", "/w/pristine", true},
		{"/w/pristine", "/w/pristine2", false},
		{"/w/slot-?", "/w/slot-a", true},
		{"/w/slot-?", "/w/slot-ab", false},
		{"/w/*", "/w/forest", true},
		{"/w/*", "/w/forest/deep", false},
		{"/w/**", "/w/forest/deep", true},
		{"/w/**/wt", "/w/a/b/wt", true},
		{"/w/a.b", "/w/axb", false},
	}
	for _, tc := range cases {
		if got := (Exception{Path: tc.glob}).Matches(tc.path); got != tc.want {
			t.Errorf("%q vs %q = %v, want %v", tc.glob, tc.path, got, tc.want)
		}
	}
}
