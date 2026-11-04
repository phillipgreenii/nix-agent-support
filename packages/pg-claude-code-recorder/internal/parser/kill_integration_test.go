//go:build integration

package parser_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// crashScenario grows six files over three rounds, parsing after each round
// (live wake-ups), then appends one more round it does NOT parse. It returns the
// ids.
func crashScenario(k *testkit.Kit) []int64 {
	const files = 6
	ids := make([]int64, files)
	for f := range ids {
		ids[f] = k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), crashLine(f, 0, 0))
	}
	drain := func() {
		for _, id := range ids {
			k.Parser.Notify(id)
		}
		if err := k.Parser.Drain(); err != nil {
			k.T.Fatal(err)
		}
	}
	drain()
	for r := 1; r <= 3; r++ {
		for f, id := range ids {
			k.Append(id, crashLine(f, r, 1)+crashLine(f, r, 2))
		}
		drain()
	}
	for f, id := range ids {
		k.Append(id, crashLine(f, 9, 1)) // copied, not yet parsed
	}
	return ids
}

const killDirEnv = "PG_CCREC_PARSER_KILL_DIR"

// referenceDump is what a clean run of the scenario plus a final parse holds.
func referenceDump(t *testing.T) string {
	t.Helper()
	ref := testkit.New(t)
	crashScenario(ref)
	ref.Parse()
	return indexDump(t, ref)
}

// TestKillNineAfterCommitsReopensConsistentAndResumes really kills a process
// (SIGKILL: no deferred cleanup, no store Close) after live commits and an
// unparsed copied tail, then reopens the directory: the index is consistent,
// the parser resumes from the stored offsets, and the records equal those of a
// run that never crashed (none duplicated, none missing).
func TestKillNineAfterCommitsReopensConsistentAndResumes(t *testing.T) {
	if dir := os.Getenv(killDirEnv); dir != "" {
		k := testkit.NewInDir(t, dir, nil)
		crashScenario(k)
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	want := referenceDump(t)
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestKillNineAfterCommitsReopensConsistentAndResumes$")
	cmd.Env = append(os.Environ(), killDirEnv+"="+dir)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != -1 {
		t.Fatalf("the child was not killed by a signal: %v", err)
	}
	k := testkit.NewInDir(t, dir, nil)
	var ok string
	if err := k.Store.Read().QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check = %q, %v", ok, err)
	}
	// Parsed so far: every file's parse offset is behind its copy offset by the
	// unparsed last line, and everything before it is stored.
	gens, err := k.Store.AllGenerations()
	if err != nil {
		t.Fatal(err)
	}
	if len(gens) != 6 {
		t.Fatalf("generations after the kill: %d, want 6", len(gens))
	}
	for _, g := range gens {
		if g.ParseOffset == 0 || g.ParseOffset >= g.CopyOffset {
			t.Fatalf("file %d parse offset %d, copy offset %d: want parsed but for the last line", g.File, g.ParseOffset, g.CopyOffset)
		}
	}
	before := k.Count(`SELECT COUNT(*) FROM records`)
	k.Parse()
	if after := k.Count(`SELECT COUNT(*) FROM records`); after != before+12 {
		t.Fatalf("resuming added %d records, want exactly the 6 unparsed lines (2 records each)", after-before)
	}
	if got := indexDump(t, k); got != want {
		t.Fatalf("records after the kill differ from a clean run\n--- got\n%s\n--- want\n%s", got, want)
	}
	// Idempotent: nothing left to parse.
	k.Parse()
	if got := indexDump(t, k); got != want {
		t.Fatal("a second parse changed the index")
	}
}
