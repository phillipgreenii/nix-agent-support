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
// (ADR 0015). It also proves the wired Exister probes the REAL ~/.claude path via
// the real encoder: with HOME pointed at a temp dir holding a transcript at the
// encoded location, Exists returns true.
func TestNewSessionDeps_wiresExister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	deps := newSessionDeps(config.Config{}, nil, nil)
	if deps.Exister == nil {
		t.Fatal("production session.Deps must wire Exister (nil → reap prunes resumable rows)")
	}

	cwd := "/Users/x/phillipg_mbp/.worktrees/s"
	csid := "abc-123-def-456"
	if deps.Exister.Exists(cwd, csid) {
		t.Fatal("Exister must be false before the transcript exists")
	}
	// Lay the transcript at the path the production encoder names. Mirror the
	// session package's encoder rule (every non-alphanumeric → '-', no collapse).
	dir := filepath.Join(home, ".claude", "projects", encodeForTest(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, csid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !deps.Exister.Exists(cwd, csid) {
		t.Errorf("wired production Exister must find the transcript under the real encoded path for %q", cwd)
	}
}

// encodeForTest mirrors session.encodeProjectDir (unexported in that package) so
// this cmd-layer test can compute the on-disk path independently. Keeping a local
// copy means a divergence between the two encoders fails this test loudly.
func encodeForTest(cwd string) string {
	b := make([]rune, 0, len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}
