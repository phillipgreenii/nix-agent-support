package serve

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

func paceCorpus(e *env, n int) {
	old := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		e.transcript(fmt.Sprintf("f%02d.jsonl", i), "{\"type\":\"user\"}\n", old)
	}
}

// Open only (no Start): drive the watcher with the daemon's root context while
// the pacer holds the sweep for a second per file, then Close. Close returns
// at once because the root context aborts the sweep; it fails if the root
// context is not what the sweep waits on.
func TestDaemonCloseAbortsSweepInFlight(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.MaxFilesPerSecond = 1
	paceCorpus(e, 40)
	d := e.open(t)
	if err := d.w.StartContext(d.ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for d.sweepIngested.Load()+d.filesCopied.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the sweep never started ingesting")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v (an aborted sweep must not fail Close)", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Close took %v with a sweep in flight", took)
	}
	if strings.Contains(e.log.String(), "watcher sweep failed") {
		t.Fatalf("an abort was logged as a failure:\n%s", e.log.String())
	}
}

// A forced sweep that a shutdown stops reports "aborted by shutdown" and does
// not log a failure.
func TestForcedSweepAbortedReportsError(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.MaxFilesPerSecond = 1
	paceCorpus(e, 40)
	d := e.open(t)
	if _, werr := d.opSweep(context.Background(), []byte(`{"force":true}`)); werr != nil {
		t.Fatal(werr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for d.sweepIngested.Load()+d.filesCopied.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the forced sweep never started ingesting")
		}
		time.Sleep(time.Millisecond)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d.forceMu.Lock()
	got, running, done := d.forceErr, d.forceRunning, d.forceDone
	d.forceMu.Unlock()
	if got != "aborted by shutdown" || running || done != 1 {
		t.Fatalf("forceErr %q running %v done %d", got, running, done)
	}
	if strings.Contains(e.log.String(), "forced_sweep_failed") {
		t.Fatalf("an abort was logged as a failure:\n%s", e.log.String())
	}
}

// The follow-up of a batch stops between its files: with an hour-long copy
// throttle it handles one file, wakes on cancel and reports the stop, and a
// cancelled context handles none.
func TestBackfillStopsBetweenFiles(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.CopyThrottle = time.Hour
	d := e.open(t)
	defer func() { _ = d.Close() }()
	var results []copier.BatchResult
	for i := 0; i < 3; i++ {
		results = append(results, copier.BatchResult{
			Path:   filepath.Join(e.claude, "projects", "example-proj", fmt.Sprintf("f%02d.jsonl", i)),
			Result: copier.Result{Action: "copied", Bytes: 5},
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- d.afterBatch(ctx, results) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("afterBatch reported a full batch after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("afterBatch ignored the cancelled context")
	}
	if n := d.filesCopied.Load(); n != 1 {
		t.Fatalf("handled %d files, want 1 (stop at the next file)", n)
	}
	if d.afterBatch(ctx, results) || d.filesCopied.Load() != 1 {
		t.Fatal("a cancelled context still handled a file")
	}
}

// A sweep aborted part-way leaves the store consistent: the next sweep ingests
// everything, and the result equals an uninterrupted run over the same corpus.
func TestSweepAbortThenNextSweepIngestsEverything(t *testing.T) {
	const n = 30
	e := newEnv(t, false)
	e.cfg.MaxFilesPerSecond = 100
	paceCorpus(e, n)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel) // inside the paced catch-up
	if err := d.w.RescanContext(ctx); !errors.Is(err, watcher.ErrSweepAborted) {
		t.Fatalf("first sweep err = %v, want ErrSweepAborted", err)
	}
	if err := d.w.Rescan(); err != nil {
		t.Fatal(err)
	}
	got, err := d.coverage("files")
	if err != nil {
		t.Fatal(err)
	}

	ref := newEnv(t, false)
	paceCorpus(ref, n)
	rd := ref.open(t)
	defer func() { _ = rd.Close() }()
	if err := rd.w.Rescan(); err != nil {
		t.Fatal(err)
	}
	want, err := rd.coverage("files")
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != want.Files || got.Files != n || got.Backfill.Copied != want.Backfill.Copied {
		t.Fatalf("after abort+resweep files %d copied %d; uninterrupted files %d copied %d", got.Files, got.Backfill.Copied, want.Files, want.Backfill.Copied)
	}
}
