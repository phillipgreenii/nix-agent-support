package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

// TestNewSessionDeps_wiresExister is the regression for CRITICAL #1: every
// production session.Deps must carry a non-nil Exister, or claudeSessionResumable
// is always false → resume never happens and `ccpool reap` prunes resumable rows
// (ADR 0015). It also proves the wired Exister probes the hook-recorded transcript
// path directly: Exists is true exactly when that path names an existing file.
func TestNewSessionDeps_wiresExister(t *testing.T) {
	deps := newSessionDeps(config.Config{}, nil, nil)
	if deps.Exister == nil {
		t.Fatal("production session.Deps must wire Exister (nil → reap prunes resumable rows)")
	}

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if deps.Exister.Exists(transcript) {
		t.Fatal("Exister must be false before the transcript file exists")
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !deps.Exister.Exists(transcript) {
		t.Errorf("wired production Exister must find the hook-recorded transcript at %q", transcript)
	}
}
