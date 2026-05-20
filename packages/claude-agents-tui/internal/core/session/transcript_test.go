package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript creates a transcript file at path whose first event is a
// custom-title record with the given title (unless title is ""). Returns
// the full path.
func writeTranscript(t *testing.T, dir, name, title string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := ""
	if title != "" {
		body = `{"type":"custom-title","customTitle":"` + title + `","sessionId":"x"}` + "\n"
	}
	body += `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveTranscriptPicksMostRecent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := writeTranscript(t, dir, "aaa.jsonl", "", time.Now().Add(-2*time.Hour))
	newer := writeTranscript(t, dir, "bbb.jsonl", "", time.Now().Add(-1*time.Minute))

	s := &Session{Cwd: "/Users/me/proj", SessionID: "ignored"}
	path, mtime, ok := ResolveTranscript(home, s)
	if !ok {
		t.Fatalf("ok=false")
	}
	if path != newer {
		t.Errorf("path=%q, want newer %q (old was %q)", path, newer, old)
	}
	if time.Since(mtime) > 2*time.Minute {
		t.Errorf("mtime too old: %v", mtime)
	}
}

func TestResolveTranscriptPrefersCustomTitleMatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Newer file WITHOUT matching title; older file WITH matching title.
	_ = writeTranscript(t, dir, "aaa.jsonl", "other-session", time.Now().Add(-1*time.Minute))
	match := writeTranscript(t, dir, "bbb.jsonl", "my-feature", time.Now().Add(-1*time.Hour))

	s := &Session{Cwd: "/Users/me/proj", Name: "my-feature"}
	path, _, ok := ResolveTranscript(home, s)
	if !ok {
		t.Fatalf("ok=false")
	}
	if path != match {
		t.Errorf("path=%q, want title-matching %q", path, match)
	}
}

func TestResolveTranscriptFallsBackWhenTitleMissing(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = writeTranscript(t, dir, "aaa.jsonl", "different", time.Now().Add(-5*time.Minute))
	newer := writeTranscript(t, dir, "bbb.jsonl", "also-different", time.Now().Add(-1*time.Minute))

	s := &Session{Cwd: "/Users/me/proj", Name: "not-found"}
	path, _, ok := ResolveTranscript(home, s)
	if !ok {
		t.Fatalf("ok=false")
	}
	if path != newer {
		t.Errorf("fallback to newest: got %q, want %q", path, newer)
	}
}

func TestResolveTranscriptReturnsFalseWhenDirMissing(t *testing.T) {
	home := t.TempDir()
	s := &Session{Cwd: "/no/such/dir"}
	if _, _, ok := ResolveTranscript(home, s); ok {
		t.Errorf("ok=true for missing dir, want false")
	}
}

func TestResolveTranscriptHandlesUnderscoreCwd(t *testing.T) {
	home := t.TempDir()
	// Slug is dash-for-both: /a/b_c → -a-b-c
	dir := filepath.Join(home, "projects", "-a-b-c")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = writeTranscript(t, dir, "x.jsonl", "", time.Now())

	s := &Session{Cwd: "/a/b_c"}
	if _, _, ok := ResolveTranscript(home, s); !ok {
		t.Errorf("ok=false, want true (slug should dash underscore)")
	}
}

func TestResolveTranscriptUsesSessionIDMatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two sessions share the same Cwd, neither has a Name.
	// sess-aaa.jsonl is older; sess-bbb.jsonl is newer.
	// ResolveTranscript for session with ID "sess-aaa" must return the older file,
	// not the newer one, because the SessionID matches the filename exactly.
	s1path := writeTranscript(t, dir, "sess-aaa.jsonl", "", time.Now().Add(-5*time.Minute))
	_ = writeTranscript(t, dir, "sess-bbb.jsonl", "", time.Now().Add(-1*time.Minute))

	s1 := &Session{Cwd: "/Users/me/proj", SessionID: "sess-aaa"}
	path, _, ok := ResolveTranscript(home, s1)
	if !ok {
		t.Fatalf("ok=false")
	}
	if path != s1path {
		t.Errorf("path=%q, want SessionID match %q", path, s1path)
	}
}
