package watcher

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
)

// The inotify backend's traversal (walkWatchTree) watches only allowed
// directories; it is OS-neutral so this runs on every host.
func TestWalkWatchTreeWatchesOnlyAllowedDirectories(t *testing.T) {
	claude := t.TempDir()
	ends := filepath.Join(t.TempDir(), "ends")
	buildClaudeTree(t, claude, ends)
	sc := scope.New(claude, ends)

	var read []string
	dirReadHook = func(d string) { read = append(read, d) }
	t.Cleanup(func() { dirReadHook = nil })

	var watched []string
	add := func(d string) error { watched = append(watched, d); return nil }
	for _, r := range []string{claude, ends} {
		if err := walkWatchTree(r, sc.DescendDir, false, add, nil); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(watched)
	if watched[0] != claude && !contains(watched, claude) {
		t.Fatalf("the configured Claude root must be watched: %v", watched)
	}
	for _, d := range watched {
		if d != claude && !sc.DescendDir(d) {
			t.Errorf("watched a directory outside the allowlist: %s", d)
		}
		if strings.Contains(d, "file-history") || strings.Contains(d, "plugins") ||
			strings.Contains(d, "spikes-") || strings.Contains(d, "memory") || strings.Contains(d, "not-a-uuid") {
			t.Errorf("decoy watched: %s", d)
		}
	}
	for _, want := range []string{
		filepath.Join(claude, "projects"), filepath.Join(claude, "sessions"),
		filepath.Join(claude, "projects", "example-project"),
		filepath.Join(claude, "projects", "example-project", exSession, "subagents"), ends,
	} {
		if !contains(watched, want) {
			t.Errorf("missing watch on %s", want)
		}
	}
	// the only directory read outside the allowlist is the configured Claude
	// root itself (its one listing finds projects/ and sessions/)
	for _, d := range read {
		if d != claude && !sc.DescendDir(d) {
			t.Errorf("read a directory outside the allowlist: %s", d)
		}
	}
}

func TestWalkWatchTreeChecksANewDirectoryAgainstThePredicate(t *testing.T) {
	claude := t.TempDir()
	buildClaudeTree(t, claude, filepath.Join(t.TempDir(), "ends"))
	sc := scope.New(claude, "")
	n := 0
	add := func(string) error { n++; return nil }
	if err := walkWatchTree(filepath.Join(claude, "file-history"), sc.DescendDir, true, add, nil); err != nil || n != 0 {
		t.Fatalf("rejected new directory was watched: n=%d err=%v", n, err)
	}
	var emitted []string
	if err := walkWatchTree(filepath.Join(claude, "projects", "example-project", exSession), sc.DescendDir, true, add,
		func(p string) { emitted = append(emitted, p) }); err != nil {
		t.Fatal(err)
	}
	if n == 0 || len(emitted) == 0 {
		t.Fatalf("allowed new directory not walked: watches=%d emitted=%v", n, emitted)
	}
	for _, p := range emitted {
		if strings.Contains(p, "/other/") {
			t.Errorf("emitted a file from a pruned directory: %s", p)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
