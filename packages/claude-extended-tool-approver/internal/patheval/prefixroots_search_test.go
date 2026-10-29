package patheval_test

// Grep and Glob over the named-prefix read roots, through the path-safety rule a
// PreToolUse hook runs, with a real home-shaped parent: a temporary HOME holding a
// .cache directory and the machine's four ruled entries (bead pg2-1peo5k).
//
// The bug that bead reported (Grep/Glob of ~/.cache/pd-* abstaining while
// $TMPDIR/pd-* was approved) was NOT in this package: the pool session's PATH
// resolved the bare, unwrapped binary, so CETA_EXTRA_READONLY_PREFIX_ROOTS never
// reached it, and $TMPDIR/pd-* was approved only because an unattended session's
// own $TMPDIR is read-write (ExtendForUnattended). The wiring is guarded by the nix
// check test-claude-baseline-ceta-wrapper; this test pins the binary's half, so the
// two halves cannot be confused again: with the variable present, the ~/.cache
// children are approved for both kinds of session, and the negative controls hold,
// except that an unattended session's own $TMPDIR is readable by design.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pathsafety"
)

const searchProject = "/nonexistent-ceta-project"

// searchFixture builds the home, its .cache children (each holding a file, as the
// reporter's did) and the environment the installed wrapper sets, and returns the
// resolved home and the session's $TMPDIR.
func searchFixture(t *testing.T) (home, tmpdir string) {
	t.Helper()
	home = patheval.ResolveRealPath(t.TempDir())
	cache := filepath.Join(home, ".cache")
	for _, child := range []string{"pd-x", "pdec-x", "p2-decomp-x", "pdx-foo", "pd"} {
		dir := filepath.Join(cache, child)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A per-user macOS temp directory shape; it need not exist (the evaluator
	// resolves through the nearest existing ancestor).
	tmpdir = "/private/var/folders/zz/ceta-prefix-search/T"
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmpdir+"/")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("WORKSPACE_ROOT", "")
	t.Setenv("GRADLE_USER_HOME", "")
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv(patheval.UnattendedExtraReadOnlyRootsEnv, "")
	c := home + "/.cache/"
	t.Setenv(patheval.ExtraReadOnlyPrefixRootsEnv, c+"p2-decomp-*:"+c+"pdec-*:"+c+"pd-*:$TMPDIR/pd-*")
	return home, tmpdir
}

func searchVerdict(t *testing.T, rule *pathsafety.Rule, tool, path string) hookio.RuleResult {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"pattern": "marker", "path": path})
	if err != nil {
		t.Fatal(err)
	}
	return hookio.Verdict(rule.Evaluate(&hookio.HookInput{ToolName: tool, ToolInput: raw, CWD: searchProject}))
}

func TestPrefixRoots_SearchToolsThroughPathSafety(t *testing.T) {
	home, tmpdir := searchFixture(t)
	cache := filepath.Join(home, ".cache")

	approved := []string{
		filepath.Join(cache, "pd-x"),
		filepath.Join(cache, "pdec-x"),
		filepath.Join(cache, "p2-decomp-x"),
		filepath.Join(cache, "pd-x", "marker.txt"),
		"~/.cache/pd-x",
		"~/.cache/p2-decomp-x/",
		tmpdir + "/pd-x",
	}
	// Negative controls for every session.
	refusedAlways := []string{
		cache,
		"~/.cache",
		filepath.Join(cache, "pdx-foo"),
		filepath.Join(cache, "pd"),
		filepath.Join(cache, "pd-"),
	}
	// Refused for an attended session only: an unattended session's own $TMPDIR is
	// read-write (ExtendForUnattended, ruling pg2-r9kv7), so these are approved there.
	refusedAttendedOnly := []string{tmpdir, tmpdir + "/pdx-foo"}

	attended := patheval.NewWithCWD(searchProject, searchProject)
	unattended := patheval.NewWithCWD(searchProject, searchProject)
	unattended.ExtendForUnattended()

	for _, session := range []struct {
		name string
		pe   *patheval.PathEvaluator
	}{{"attended", attended}, {"unattended", unattended}} {
		rule := pathsafety.New(session.pe)
		for _, tool := range []string{"Grep", "Glob"} {
			for _, p := range approved {
				if got := searchVerdict(t, rule, tool, p); got.Decision != hookio.Approve {
					t.Errorf("%s %s %s: %s (%s), want approve", session.name, tool, p, got.Decision, got.Reason)
				}
			}
			for _, p := range refusedAlways {
				if got := searchVerdict(t, rule, tool, p); got.Decision == hookio.Approve {
					t.Errorf("%s %s %s: approved (%s), want not approved", session.name, tool, p, got.Reason)
				}
			}
			for _, p := range refusedAttendedOnly {
				got := searchVerdict(t, rule, tool, p)
				if session.name == "attended" && got.Decision == hookio.Approve {
					t.Errorf("attended %s %s: approved (%s), want not approved", tool, p, got.Reason)
				}
				if session.name == "unattended" && got.Decision != hookio.Approve {
					t.Errorf("unattended %s %s: %s (%s), want approve (the session's own TMPDIR)", tool, p, got.Decision, got.Reason)
				}
			}
		}
	}
}

// TestPrefixRoots_SearchToolsWithoutTheVariable is the shape the pool sessions had:
// the binary run without CETA_EXTRA_READONLY_PREFIX_ROOTS (the unwrapped package).
// The ~/.cache children are refused, while an unattended session's $TMPDIR child is
// still approved through the TMPDIR grant, which is exactly the misleading split
// pg2-1peo5k observed.
func TestPrefixRoots_SearchToolsWithoutTheVariable(t *testing.T) {
	home, tmpdir := searchFixture(t)
	t.Setenv(patheval.ExtraReadOnlyPrefixRootsEnv, "")
	pe := patheval.NewWithCWD(searchProject, searchProject)
	pe.ExtendForUnattended()
	rule := pathsafety.New(pe)
	for _, tool := range []string{"Grep", "Glob"} {
		if got := searchVerdict(t, rule, tool, filepath.Join(home, ".cache", "pd-x")); got.Decision == hookio.Approve {
			t.Errorf("%s ~/.cache/pd-x approved without the variable (%s)", tool, got.Reason)
		}
		if got := searchVerdict(t, rule, tool, tmpdir+"/pd-x"); got.Decision != hookio.Approve {
			t.Errorf("%s $TMPDIR/pd-x: %s (%s), want approve through the unattended TMPDIR grant", tool, got.Decision, got.Reason)
		}
	}
}
