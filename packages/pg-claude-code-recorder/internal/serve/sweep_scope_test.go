package serve

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// A daemon sweeping a Claude directory that holds every decoy class ingests
// nothing from a decoy and never opens a decoy directory.
func TestSweepIngestsNoDecoyAndOpensNoDecoyDirectory(t *testing.T) {
	e := newEnv(t, true)
	const sess = "0f8fad5b-d9cb-469f-a165-70867728950e"
	proj := "projects/example-proj/"
	accepted := []string{
		"history.jsonl",
		"sessions/100.json",
		proj + sess + ".jsonl",
		proj + sess + ".status.jsonl",
		proj + sess + "/subagents/agent-1.jsonl",
		proj + sess + "/tool-results/r1.txt",
	}
	decoys := []string{
		proj + "spikes-example/a.json",
		proj + "spikes-example/deep/b.json",
		proj + "memory/m.md",
		proj + "artifacts/a.bin",
		proj + "not-a-uuid/subagents/agent-1.jsonl",
		proj + sess + "/other/x.json",
		proj + sess + "/scratchpad/claim.2.json",
		"file-history/h1/v1",
		"plugins/example/a/b.js",
		"paste-cache/p1",
		"security/s1",
		"usage-data/u1",
		"settings.json",
	}
	for _, rel := range append(append([]string{}, accepted...), decoys...) {
		p := filepath.Join(e.claude, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()

	var mu sync.Mutex
	var read []string
	restore := watcher.SetDirReadHook(func(dir string) { mu.Lock(); read = append(read, dir); mu.Unlock() })
	defer restore()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}

	cov, err := d.coverage("files")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range cov.FileList {
		got = append(got, strings.TrimPrefix(f.Path, e.claude+"/"))
	}
	sort.Strings(got)
	want := append([]string{}, accepted...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ingested set\n got: %v\nwant: %v", got, want)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, dir := range read {
		rel := strings.TrimPrefix(dir, e.claude)
		for _, bad := range []string{"spikes-", "/memory", "/artifacts", "not-a-uuid", "/other", "/scratchpad", "file-history", "plugins", "paste-cache", "security", "usage-data"} {
			if strings.Contains(rel, bad) {
				t.Errorf("a decoy directory was opened: %s", dir)
			}
		}
		if dir == e.claude {
			t.Errorf("the Claude directory itself was walked")
		}
	}
	if len(read) == 0 {
		t.Fatal("directory-read seam saw nothing")
	}
}

// A file renamed from an ingested name to a name the allowlist rejects, inside a
// directory the sweep still walks, is the same file: the sweep keeps its
// identity, so the old path is closed and the file is NOT tombstoned as deleted.
func TestRenameToARejectedNameIsNotATombstone(t *testing.T) {
	e := newEnv(t, false)
	const sess = "0f8fad5b-d9cb-469f-a165-70867728950e"
	old := e.transcript(sess+".jsonl", "line\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(filepath.Dir(old), ".orphaned-"+sess+".jsonl")
	if err := os.Rename(old, renamed); err != nil {
		t.Fatal(err)
	}
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	files, err := d.Store().AllFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v err %v", files, err)
	}
	if files[0].State == "deleted" {
		t.Fatalf("a rename to a rejected name was tombstoned as a deletion: %+v", files[0])
	}
	if paths, _ := d.Store().CurrentPaths(files[0].ID); len(paths) != 0 {
		t.Fatalf("the old path should be closed, got %v", paths)
	}
}
