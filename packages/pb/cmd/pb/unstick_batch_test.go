package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

func TestBatch_writesBatchAndFacts(t *testing.T) {
	w := preparedWorkdir(t)
	f := run.NewFakeRunner() // batch is local: it must not call bd
	out, _, err := execUnstick(testEnv(f), "", "batch", "--workdir", w, "--name", "FOLLOWUPS", "--ids", "a-4,a-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("batch must not run commands, got %v", f.Calls())
	}
	if got := readFile(t, filepath.Join(w, "batches", "FOLLOWUPS")); got != "a-2\na-4\n" {
		t.Errorf("batches/FOLLOWUPS = %q (want sorted ids)", got)
	}
	var facts []unstick.Fact
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(w, "facts", "FOLLOWUPS.json"))), &facts); err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].ID != "a-2" || facts[1].ID != "a-4" {
		t.Errorf("facts = %+v", facts)
	}
	if !strings.Contains(out, "batch FOLLOWUPS: 2 bead(s)") {
		t.Errorf("human output = %q", out)
	}

	out, _, err = execUnstick(testEnv(f), "", "batch", "--workdir", w, "--name", "F2", "--ids", "a-3", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res batchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Name != "F2" || res.Size != 1 || res.Batch != filepath.Join(w, "batches", "F2") || res.Facts != filepath.Join(w, "facts", "F2.json") {
		t.Errorf("json result = %+v", res)
	}
}

func TestBatch_validation(t *testing.T) {
	w := preparedWorkdir(t)
	empty := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no workdir", []string{"batch", "--name", "N", "--ids", "a-1"}, "--workdir is required"},
		{"relative workdir", []string{"batch", "--workdir", "rel", "--name", "N", "--ids", "a-1"}, "absolute"},
		{"missing workdir dir", []string{"batch", "--workdir", filepath.Join(w, "nope"), "--name", "N", "--ids", "a-1"}, "not an existing directory"},
		{"no name", []string{"batch", "--workdir", w, "--ids", "a-1"}, "--name is required"},
		{"no ids", []string{"batch", "--workdir", w, "--name", "N"}, "--ids is required"},
		{"path-shaped name", []string{"batch", "--workdir", w, "--name", "../x", "--ids", "a-1"}, "invalid batch name"},
		{"unknown id", []string{"batch", "--workdir", w, "--name", "N", "--ids", "a-1,zz-9"}, "zz-9"},
		{"no export in workdir", []string{"batch", "--workdir", empty, "--name", "N", "--ids", "a-1"}, "export.jsonl"},
	}
	for _, c := range cases {
		_, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", c.args...)
		if err == nil || exitCodeFor(err) != 1 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: exit %d, err = %v; want exit 1 mentioning %q", c.name, exitCodeFor(err), err, c.want)
		}
	}
	// A rejected batch leaves nothing behind.
	for _, n := range []string{"N", "x"} {
		if _, err := os.Stat(filepath.Join(w, "batches", n)); err == nil {
			t.Errorf("batches/%s was written by a rejected call", n)
		}
	}
}
