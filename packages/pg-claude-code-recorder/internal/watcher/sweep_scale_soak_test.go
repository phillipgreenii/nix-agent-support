//go:build soak

package watcher

// Scale test of the pruning sweep (V-4: not a unit test, soak tag only). It
// builds a copy-shaped Claude directory with more than 100,000 files, most of
// them in trees the allowlist never opens, and compares an unpruned walk with
// the pruned sweep. It asserts a RATIO and the exact accepted count, never an
// absolute time, because host load varies. It runs in the soak check
// (`pg-claude-code-recorder-soak`, which selects tests named TestSoak*):
//
//	go test -tags soak -run TestSoakPrunedSweep -v ./internal/watcher/

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
)

func TestSoakPrunedSweep(t *testing.T) {
	claude := t.TempDir()
	mk := func(rel string, n, perDir int) {
		for i := 0; i < n; i++ {
			dir := filepath.Join(claude, rel, fmt.Sprintf("d%04d", i/perDir))
			if i%perDir == 0 {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("file-history", 60000, 200)
	mk("plugins", 30000, 200)
	mk("security", 10000, 200)
	mk("paste-cache", 2000, 200)
	mk("projects/example-project/spikes-example", 20000, 200)
	accepted := 0
	proj := filepath.Join(claude, "projects", "example-project")
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("%08x-d9cb-469f-a165-70867728950e", i)
		for _, rel := range []string{id + ".jsonl", id + "/subagents/agent-1.jsonl", id + "/tool-results/r1.txt"} {
			p := filepath.Join(proj, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			accepted++
		}
	}
	total := 60000 + 30000 + 10000 + 2000 + 20000 + accepted
	if total < 100000 {
		t.Fatalf("synthetic tree has %d files, want at least 100000", total)
	}
	sc := scope.New(claude, "")

	t0 := time.Now()
	all, err := platformSweep(claude, nil)
	if err != nil {
		t.Fatal(err)
	}
	full := time.Since(t0)
	if len(all) != total {
		t.Fatalf("unpruned walk saw %d files, want %d", len(all), total)
	}

	t0 = time.Now()
	n := 0
	for _, root := range sc.SweepDirs() {
		infos, err := platformSweep(root, sc.DescendDir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, fi := range infos {
			if sc.Match(fi.Path).Ingest {
				n++
			}
		}
	}
	pruned := time.Since(t0)
	if n != accepted {
		t.Fatalf("pruned sweep kept %d files, want %d", n, accepted)
	}
	t.Logf("SUMMARY files=%d accepted=%d unpruned=%s pruned=%s", total, accepted, full, pruned)
	if pruned*5 > full {
		t.Fatalf("pruned sweep %s is not at least 5x faster than the unpruned walk %s", pruned, full)
	}
}
