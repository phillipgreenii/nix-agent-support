package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFSSessionExister_statsTheRecordedPath pins the production probe: it reports
// true exactly when the recorded transcript path names an existing regular file,
// and false when the path is absent or names a directory (ADR 0015). The path is
// the authoritative hook-recorded transcript path, not a reconstructed guess.
func TestFSSessionExister_statsTheRecordedPath(t *testing.T) {
	ex := NewFSSessionExister() // production constructor, not a fake
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	// Absent → false (nothing to resume).
	if ex.Exists(transcript) {
		t.Fatal("must be false before the transcript file exists")
	}

	// Regular file present → true.
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ex.Exists(transcript) {
		t.Errorf("must be true once the transcript file %q exists", transcript)
	}

	// A directory at the path → false (a transcript is a file, not a dir).
	subdir := filepath.Join(dir, "adir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if ex.Exists(subdir) {
		t.Error("must be false when the path names a directory, not a transcript file")
	}
}

// TestFSSessionExister_falseOnEmptyPath: an empty path is never resumable.
func TestFSSessionExister_falseOnEmptyPath(t *testing.T) {
	if NewFSSessionExister().Exists("") {
		t.Error("empty transcript path must be false (nothing to resume)")
	}
}
