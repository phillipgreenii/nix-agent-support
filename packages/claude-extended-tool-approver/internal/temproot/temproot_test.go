package temproot

import (
	"os"
	"slices"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// temproot_test.go pins the CETA_EXTRA_TEMP_ROOTS contract (pg2-t0hi5).
//
// The roots below are DELIBERATELY outside every built-in root. t.TempDir()
// would be wrong here: it lives under $TMPDIR (a built-in root), so a path
// "under an extra root" would be Under even if Roots() ignored the variable,
// and the sibling case would be wrongly Under too, making the tests vacuous.
// /etc always exists, and a NONEXISTENT child of it is fine because
// patheval.ResolveRealPath reattaches the missing suffix to the nearest real
// ancestor.
const (
	extraA = "/etc/ceta-x-a"
	extraB = "/etc/ceta-x-b"
)

// TestRoots_ExtraTempRoots_Merged: each colon-separated entry is added,
// realpath-resolved, after the built-ins.
func TestRoots_ExtraTempRoots_Merged(t *testing.T) {
	t.Setenv(extraRootsEnvVar, extraA+":"+extraB)
	got := Roots()
	for _, want := range []string{extraA, extraB} {
		r := patheval.ResolveRealPath(want)
		if !slices.Contains(got, r) {
			t.Errorf("Roots() = %v, want it to contain %q", got, r)
		}
	}
}

// TestUnder_ExtraTempRoot covers the directory-boundary match against an
// extra root: a descendant is Under, the root itself is Under, a sibling that
// merely shares a name prefix is NOT.
func TestUnder_ExtraTempRoot(t *testing.T) {
	t.Setenv(extraRootsEnvVar, extraA+":"+extraB)
	tests := []struct {
		path string
		want bool
	}{
		{extraA + "/sub/f", true},
		{extraA, true},
		{extraB + "/deep/er/file", true},
		{extraA + "-other", false},
		{extraA + "-other/sub", false},
		{"/etc/ceta-x-c/sub", false},
	}
	for _, tt := range tests {
		if got := Under(tt.path); got != tt.want {
			t.Errorf("Under(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestUnder_ExtraTempRoot_UnsetAndEmptyAddNothing: with the variable unset or
// empty, a path under the would-be extra root is not Under.
func TestUnder_ExtraTempRoot_UnsetAndEmptyAddNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
	}{{"unset", false}, {"empty", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(extraRootsEnvVar, "")
			} else {
				t.Setenv(extraRootsEnvVar, "x")
				// t.Setenv registered restoration of the prior value; Unsetenv now
				// leaves it genuinely unset for the body of this subtest.
				if err := os.Unsetenv(extraRootsEnvVar); err != nil {
					t.Fatal(err)
				}
			}
			if Under(extraA + "/sub/f") {
				t.Errorf("Under(%q) = true with %s %s, want false", extraA+"/sub/f", extraRootsEnvVar, tc.name)
			}
			if slices.Contains(Roots(), patheval.ResolveRealPath(extraA)) {
				t.Errorf("Roots() contains %q with %s %s", extraA, extraRootsEnvVar, tc.name)
			}
		})
	}
}

// TestRoots_ExtraTempRoots_EmptyEntrySkipped: `a::b` skips the empty entry
// (it must not turn into "" or into the process cwd) and keeps both roots.
func TestRoots_ExtraTempRoots_EmptyEntrySkipped(t *testing.T) {
	t.Setenv(extraRootsEnvVar, extraA+"::"+extraB+":")
	got := Roots()
	for _, want := range []string{extraA, extraB} {
		if r := patheval.ResolveRealPath(want); !slices.Contains(got, r) {
			t.Errorf("Roots() = %v, want it to contain %q", got, r)
		}
	}
	if slices.Contains(got, "") {
		t.Errorf("Roots() = %v contains an empty root", got)
	}
}

// TestRoots_ExtraTempRoots_TrailingSlashCleaned: a trailing slash on an
// entry yields the same cleaned root, still matched on a directory boundary.
func TestRoots_ExtraTempRoots_TrailingSlashCleaned(t *testing.T) {
	t.Setenv(extraRootsEnvVar, extraA+"/")
	got := Roots()
	if want := patheval.ResolveRealPath(extraA); !slices.Contains(got, want) {
		t.Errorf("Roots() = %v, want the cleaned root %q", got, want)
	}
	if slices.Contains(got, extraA+"/") {
		t.Errorf("Roots() = %v keeps the trailing slash", got)
	}
	if !Under(extraA + "/sub/f") {
		t.Errorf("Under(%q) = false, want true", extraA+"/sub/f")
	}
	if Under(extraA + "-other") {
		t.Errorf("Under(%q) = true, want false", extraA+"-other")
	}
}
