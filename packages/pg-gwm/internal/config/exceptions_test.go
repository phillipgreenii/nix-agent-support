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

// TestGitBranchMaintenanceExceptionPatterns pins the standing exception patterns that the ZR
// machine config lists for the git-branch-maintenance temp worktree (/tmp/git-branch-maintenance-<pid>
// in the tool's script) against the real path forms: the root and its contents, under both
// spellings of /tmp (the matcher does no symlink resolution), with siblings left unmatched.
func TestGitBranchMaintenanceExceptionPatterns(t *testing.T) {
	var globs []string
	for _, tmp := range []string{"/private/tmp", "/tmp"} {
		globs = append(globs, tmp+"/git-branch-maintenance-*", tmp+"/git-branch-maintenance-*/**")
	}
	matches := func(p string) bool {
		for _, g := range globs {
			if (Exception{Path: g}).Matches(p) {
				return true
			}
		}
		return false
	}
	for _, p := range []string{
		"/tmp/git-branch-maintenance-3396",
		"/tmp/git-branch-maintenance-3396/src/file.go",
		"/private/tmp/git-branch-maintenance-3396",
		"/private/tmp/git-branch-maintenance-3396/src/file.go",
	} {
		if !matches(p) {
			t.Errorf("%q should match", p)
		}
	}
	for _, p := range []string{
		"/private/tmp/other",
		"/tmp/other",
		"/private/tmp/other/git-branch-maintenance-1",
		"/private/tmp/git-branch-maintenance",
		"/private/tmp/n3396",
	} {
		if matches(p) {
			t.Errorf("%q should not match", p)
		}
	}
}
