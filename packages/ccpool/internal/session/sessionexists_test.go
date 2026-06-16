package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEncodeProjectDir pins Claude's cwd→project-dir encoding: every character
// that is not [A-Za-z0-9] is replaced with '-', and runs are NOT collapsed
// (so adjacent specials like "/." yield "--"), matching
// ~/.claude/projects/<encoded-cwd>/ on disk (ADR 0015). Verified against real
// entries, e.g. /Users/phillipg/gc/.gc-worktrees/... →
// -Users-phillipg-gc--gc-worktrees-..., and phillipg_mbp → phillipg-mbp.
func TestEncodeProjectDir(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/Volumes/ziprecruiter/monorepo", "-Volumes-ziprecruiter-monorepo"},
		{"/tmp/proj", "-tmp-proj"},
		{"/", "-"},
		{"relative/path", "relative-path"},
		// Underscores are not alphanumeric → become '-' (real: phillipg_mbp → phillipg-mbp).
		{"/Users/x/phillipg_mbp/.worktrees/s", "-Users-x-phillipg-mbp--worktrees-s"},
		// Adjacent specials are NOT collapsed: "/." → "--" (real: gc/.gc-worktrees → gc--gc-worktrees).
		{"/a/b_c/.d", "-a-b-c--d"},
		// Dots, underscores, and other punctuation all map to '-' without collapsing.
		{"/Users/phillipg/gc/.gc-worktrees/x", "-Users-phillipg-gc--gc-worktrees-x"},
	}
	for _, tc := range cases {
		if got := encodeProjectDir(tc.cwd); got != tc.want {
			t.Errorf("encodeProjectDir(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

// TestClaudeSessionExists_trueWhenTranscriptPresent proves the probe: the helper
// reports true exactly when <home>/.claude/projects/<encoded-cwd>/<csid>.jsonl
// exists, false otherwise — using a temp HOME so it never touches a real ~/.claude.
func TestClaudeSessionExists_trueWhenTranscriptPresent(t *testing.T) {
	home := t.TempDir()
	cwd := "/Volumes/work/repo"
	csid := "abc-123"

	if claudeSessionExists(home, cwd, csid) {
		t.Fatal("must be false before the transcript exists")
	}

	dir := filepath.Join(home, ".claude", "projects", encodeProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, csid+".jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !claudeSessionExists(home, cwd, csid) {
		t.Errorf("must be true once %s/%s.jsonl exists", dir, csid)
	}
	// A different csid under the same project dir is still false.
	if claudeSessionExists(home, cwd, "other-csid") {
		t.Error("must be false for a csid with no transcript")
	}
}

func TestClaudeSessionExists_falseOnEmptyInputs(t *testing.T) {
	home := t.TempDir()
	if claudeSessionExists(home, "/cwd", "") {
		t.Error("empty csid must be false (nothing to resume)")
	}
	if claudeSessionExists("", "/cwd", "csid") {
		t.Error("empty home must be false")
	}
}
