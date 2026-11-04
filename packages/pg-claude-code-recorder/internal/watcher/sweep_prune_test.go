package watcher

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
)

const (
	exSession = "0f8fad5b-d9cb-469f-a165-70867728950e"
	exOther   = "1a8fad5b-d9cb-469f-a165-70867728950f"
)

// buildClaudeTree writes one file of every accepted kind and a decoy for every
// rejected class, and returns the sorted relative paths that MUST be swept.
func buildClaudeTree(t *testing.T, claude, ends string) []string {
	t.Helper()
	proj := "projects/example-project/"
	accepted := []string{
		"history.jsonl",
		"sessions/100.json",
		proj + exSession + ".jsonl",
		proj + exSession + ".status.jsonl",
		proj + exSession + ".session-mode.json",
		proj + exSession + ".compaction.json",
		proj + exSession + "/subagents/agent-1.jsonl",
		proj + exSession + "/subagents/agent-1.meta.json",
		proj + exSession + "/tool-results/r1.txt",
		proj + exSession + "/tool-results/deep/r2.txt",
		proj + exOther + "/scratchpad/s.md",
		proj + exOther + "/workflows/w.json",
	}
	decoys := []string{
		proj + "spikes-example/a.json",
		proj + "spikes-example/deep/b.json",
		proj + "memory/m.md",
		proj + "artifacts/a.bin",
		proj + "not-a-uuid/subagents/agent-1.jsonl",
		proj + exSession + "/other/x.json",
		proj + exSession + "/loose.json",
		proj + "sessions-index.json",
		"file-history/h1/v1",
		"plugins/example/a/b.js",
		"paste-cache/p1",
		"security/s1",
		"usage-data/u1",
		"settings.json",
	}
	for _, rel := range append(append([]string{}, accepted...), decoys...) {
		write(t, filepath.Join(claude, rel), "x")
	}
	write(t, filepath.Join(ends, "e1.json"), "{}")
	want := append([]string{}, accepted...)
	return want
}

type sweepFn func(root string, descend func(string) bool) ([]FileInfo, error)

func runPrunedSweep(t *testing.T, name string, sweep sweepFn) {
	t.Run(name, func(t *testing.T) {
		claude := t.TempDir()
		ends := filepath.Join(t.TempDir(), "ends")
		want := buildClaudeTree(t, claude, ends)
		sc := scope.New(claude, ends)

		var read []string
		dirReadHook = func(d string) { read = append(read, d) }
		t.Cleanup(func() { dirReadHook = nil })

		var got []string
		for _, root := range sc.SweepDirs() {
			infos, err := sweep(root, sc.DescendDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, fi := range infos {
				// the walker prunes directories; the watcher's Filter then drops
				// the stray files inside an allowed directory
				if sc.Match(fi.Path).Ingest {
					got = append(got, fi.Path)
				}
			}
		}
		for _, p := range sc.SweepFiles() {
			if _, ok, err := lstatInfo(p); err != nil || !ok {
				t.Fatalf("history lstat: %v %v", ok, err)
			}
			got = append(got, p)
		}
		// the control ends file is swept through the ends root
		wantAbs := []string{filepath.Join(ends, "e1.json")}
		for _, r := range want {
			wantAbs = append(wantAbs, filepath.Join(claude, r))
		}
		sort.Strings(got)
		sort.Strings(wantAbs)
		if strings.Join(got, "\n") != strings.Join(wantAbs, "\n") {
			t.Fatalf("swept set differs\n got: %v\nwant: %v", got, wantAbs)
		}
		// no directory outside the allowlist was opened
		for _, d := range read {
			if !sc.DescendDir(d) {
				t.Errorf("opened directory outside the allowlist: %s", d)
			}
			if d == claude {
				t.Errorf("walked the Claude directory itself")
			}
		}
		if len(read) == 0 {
			t.Fatal("directory-read seam saw nothing")
		}
	})
}

func TestPrunedSweepWalkSweep(t *testing.T) { runPrunedSweep(t, "walkSweep", walkSweep) }

// platformSweep is getattrlistbulk on darwin with cgo, so this covers bulkWalk
// there and walkSweep elsewhere.
func TestPrunedSweepPlatform(t *testing.T) { runPrunedSweep(t, "platformSweep", platformSweep) }

func TestWatcherSweepsTargetsOnly(t *testing.T) {
	claude := t.TempDir()
	ends := filepath.Join(t.TempDir(), "ends")
	buildClaudeTree(t, claude, ends)
	sc := scope.New(claude, ends)
	w, sink := newTestWatcher(t, Config{
		Roots:      []string{claude, ends},
		SweepDirs:  sc.SweepDirs(),
		SweepFiles: sc.SweepFiles(),
		DirFilter:  sc.DescendDir,
		Filter:     func(p string) bool { return sc.Match(p).Ingest },
		backend:    &fakeBackend{},
	})
	var read []string
	dirReadHook = func(d string) { read = append(read, d) }
	t.Cleanup(func() { dirReadHook = nil })
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.paths()); n != 13 {
		t.Errorf("swept %d files, want 13", n)
	}
	for _, d := range read {
		if d == claude || !sc.DescendDir(d) {
			t.Errorf("read %s", d)
		}
	}
}

func TestMissingHistoryFileIsSkipped(t *testing.T) {
	claude := t.TempDir()
	sc := scope.New(claude, "")
	w, sink := newTestWatcher(t, Config{
		Roots: []string{claude}, SweepDirs: sc.SweepDirs(), SweepFiles: sc.SweepFiles(),
		DirFilter: sc.DescendDir, backend: &fakeBackend{},
	})
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.paths()); n != 0 {
		t.Fatalf("events %d", n)
	}
}
