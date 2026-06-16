package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEncodeProjectDir pins Claude's cwd→project-dir encoding: each run of the
// OS path separator collapses to a single '-' (a leading separator yields a
// leading '-'), matching ~/.claude/projects/<encoded-cwd>/ on disk (ADR 0015).
func TestEncodeProjectDir(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/Volumes/ziprecruiter/monorepo", "-Volumes-ziprecruiter-monorepo"},
		{"/tmp/proj", "-tmp-proj"},
		{"/", "-"},
		{"relative/path", "relative-path"},
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
