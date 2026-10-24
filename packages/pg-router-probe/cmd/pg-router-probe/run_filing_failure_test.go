package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Subject: a failed bd write for a finding (create, metadata update, comment)
// ends the run through the partial contract (exit 4) instead of a stderr
// warning plus exit 0, and a failed binary-hash write keeps the snapshot
// baseline at the previous hash so the finding is derived again (pg2-a7pz9).

var errBdWrite = errors.New("bd write lock held")

// changedBinaryOpts writes a baseline snapshot for a binary, then rewrites the
// binary, so the next run derives a binary-hash-mismatch finding. It returns
// the options, the previous hash, and the new hash.
func changedBinaryOpts(t *testing.T) (opts runOptions, prevHash, newHash string) {
	t.Helper()
	dir := t.TempDir()
	bin := writeBinary(t, dir, "handler", "v1")
	opts = baseOpts(t)
	opts.binaryPath = bin
	cmd, _ := testCmd()
	if err := runProbe(cmd, opts, (&spyDeps{}).toRunDeps(time.Now())); err != nil {
		t.Fatalf("baseline run: %v", err)
	}
	var err error
	if prevHash, err = hashFile(bin); err != nil {
		t.Fatal(err)
	}
	writeBinary(t, dir, "handler", "v2-changed")
	if newHash, err = hashFile(bin); err != nil {
		t.Fatal(err)
	}
	return opts, prevHash, newHash
}

func TestFailedBinaryHashCreateIsPartialAndRetriedNextRun(t *testing.T) {
	opts, prevHash, newHash := changedBinaryOpts(t)

	failing := &spyDeps{createErr: errBdWrite}
	cmd, _ := testCmd()
	err := runProbe(cmd, opts, failing.toRunDeps(time.Now()))
	if got := exitCodeOf(t, err); got != 4 {
		t.Fatalf("failed create: exit %d (err=%v), want 4", got, err)
	}
	if len(failing.created) != 1 {
		t.Fatalf("expected one create attempt, got %+v", failing.created)
	}
	snap, ok := loadSnapshot(opts.snapshotPath)
	if !ok || snap.BinaryHash != prevHash {
		t.Fatalf("baseline advanced to %q (ok=%v) despite the failed create, want previous %q", snap.BinaryHash, ok, prevHash)
	}

	// Same current binary hash: the finding must be derived again.
	retry := &spyDeps{}
	cmd2, _ := testCmd()
	if err := runProbe(cmd2, opts, retry.toRunDeps(time.Now())); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if len(retry.created) != 1 {
		t.Fatalf("expected the binary-hash finding to be filed again, got %+v", retry.created)
	}
	snap, _ = loadSnapshot(opts.snapshotPath)
	if snap.BinaryHash != newHash {
		t.Fatalf("baseline after a successful create = %q, want %q", snap.BinaryHash, newHash)
	}

	// And once filed, the next run is quiet and clean.
	quiet := &spyDeps{}
	cmd3, _ := testCmd()
	if err := runProbe(cmd3, opts, quiet.toRunDeps(time.Now())); err != nil || len(quiet.created) != 0 {
		t.Fatalf("third run: err=%v created=%+v, want clean and silent", err, quiet.created)
	}
}

// An open bead whose recorded hash differs gets an update + comment
// (actionUpdate); either write failing is exit 4 and holds the baseline.
func TestFailedBinaryHashUpdateWritesArePartial(t *testing.T) {
	cases := []struct {
		name string
		spy  func() *spyDeps
	}{
		{"metadata update fails", func() *spyDeps { return &spyDeps{updateErr: errBdWrite} }},
		{"comment fails", func() *spyDeps { return &spyDeps{commentErr: errBdWrite} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, prevHash, _ := changedBinaryOpts(t)
			spy := c.spy()
			spy.listEscalatedResult = []connectorIssue{{
				ID:       "zr-open",
				Labels:   []string{"escalated"},
				Metadata: map[string]string{metaFingerprint: binaryHashMismatchFingerprint, metaState: "stale-hash"},
			}}
			cmd, _ := testCmd()
			err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
			if got := exitCodeOf(t, err); got != 4 {
				t.Fatalf("exit %d (err=%v), want 4", got, err)
			}
			if len(spy.updated) != 1 || len(spy.commented) != 1 {
				t.Fatalf("expected one update and one comment attempt, got updated=%+v commented=%+v", spy.updated, spy.commented)
			}
			snap, _ := loadSnapshot(opts.snapshotPath)
			if snap.BinaryHash != prevHash {
				t.Fatalf("baseline = %q, want held at %q", snap.BinaryHash, prevHash)
			}
		})
	}
}

// A failed create for a Grafana finding is exit 4 too (it is retried anyway,
// since no episode is recorded).
func TestFailedGrafanaCreateIsPartial(t *testing.T) {
	opts := episodeOpts(t)
	opts.snapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	spy := episodeSpy(started, "B=5", nil, nil)
	spy.createErr = errBdWrite
	err := tick(t, opts, spy, t0)
	if got := exitCodeOf(t, err); got != 4 {
		t.Fatalf("exit %d (err=%v), want 4", got, err)
	}
}
