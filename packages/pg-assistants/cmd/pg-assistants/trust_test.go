package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrustWritesClaudeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	before := `{"numStartups": 3, "projects": {"/example/other": {"allowedTools": ["x"]}}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "trust", "--claude-json", path, "/example/work/a", "/example/work/b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, stdout, stderr)
	}
	var got struct {
		NumStartups int                       `json:"numStartups"`
		Projects    map[string]map[string]any `json:"projects"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{"/example/work/a", "/example/work/b"} {
		if got.Projects[cwd]["hasTrustDialogAccepted"] != true {
			t.Errorf("%s is not trusted in %s", cwd, data)
		}
	}
	if got.NumStartups != 3 || got.Projects["/example/other"]["allowedTools"] == nil {
		t.Errorf("other content was not kept: %s", data)
	}
}

func TestTrustErrors(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, ".claude.json")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unparseable file", []string{"--claude-json", broken, "/example/work/a"}, 1},
		{"no directories", []string{"--claude-json", good}, 2},
		{"no --claude-json", []string{"/example/work/a"}, 2},
		{"relative --claude-json", []string{"--claude-json", "claude.json", "/example/work/a"}, 2},
		{"relative directory", []string{"--claude-json", good, "work/a"}, 2},
		{"unknown flag", []string{"--bogus"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, append([]string{"trust"}, tc.args...)...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.want, stderr)
			}
			if stderr == "" {
				t.Errorf("no message on stderr")
			}
		})
	}
	if _, err := os.Stat(good); err == nil {
		t.Errorf("a refused trust wrote %s", good)
	}
	if code, stdout, _ := runCLI(t, "trust", "--help"); code != 0 || !strings.Contains(stdout, "--claude-json") {
		t.Errorf("trust --help = %d, %q; want 0 and the help", code, stdout)
	}
}

// claudetrust's read-merge-rename takes no lock, so the daemon's adapter MUST
// run one Ensure at a time: every loop that starts at once would otherwise
// write the file from a read that lacks the others' directories.
//
// This runs on the real clock, not in a synctest bubble: a goroutine waiting
// on a sync.Mutex is not durably blocked, so a bubble's clock would never
// advance past the holder's sleep. The sleep only holds the window open while
// the other callers are scheduled; with the mutex the answer is always 1.
func TestTrustFileSerialisesEnsure(t *testing.T) {
	var inside, most atomic.Int32
	tf := &trustFile{path: "/example/home/.claude.json", ensure: func(path string, cwds ...string) error {
		n := inside.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond) // an unserialised caller is in here meanwhile
		inside.Add(-1)
		return nil
	}}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := tf.Ensure(fmt.Sprintf("/example/work/%d", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := most.Load(); got != 1 {
		t.Errorf("%d Ensure calls ran at once, want 1", got)
	}
}

func TestTrustFileWritesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	tf := newTrustFile(path)
	if ok, err := tf.IsTrusted("/example/work/a"); err != nil || ok {
		t.Fatalf("IsTrusted before = %v, %v; want false, nil", ok, err)
	}
	if err := tf.Ensure("/example/work/a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := tf.IsTrusted("/example/work/a"); err != nil || !ok {
		t.Errorf("IsTrusted after = %v, %v; want true, nil", ok, err)
	}
}
