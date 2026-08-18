package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestTranscriptHasTitleSurvivesOversizedEarlyLine is a regression test for a
// bug where transcriptHasTitle used a scanner ceiling of only 1 MiB
// (scanner.Buffer(make([]byte, 1<<16), 1<<20) -- bufio.Scanner.Buffer's
// effective max is the LARGER of its two args) while every other transcript
// reader in this repo uses a 1 MiB initial / 16 MiB ceiling. A single early
// JSONL line over 1 MiB (e.g. a large tool_result or a pasted file on one
// line) made bufio.Scanner.Scan stop and set scanner.Err() to
// bufio.ErrTooLong, which transcriptHasTitle never checked -- so a genuine
// custom-title record later in the scan window was silently never seen,
// indistinguishable from "no matching custom-title record".
//
// This test builds a transcript whose FIRST line is ~2 MiB (over the OLD 1
// MiB ceiling, under the NEW 16 MiB one) followed by a genuine custom-title
// record within the titleScanLines window, and asserts transcriptHasTitle
// still finds it. Reverting the buffer ceiling to the old
// scanner.Buffer(make([]byte, 1<<16), 1<<20) makes this test fail: Scan stops
// at the oversized first line, never reaches the custom-title record, and
// transcriptHasTitle returns false.
func TestTranscriptHasTitleSurvivesOversizedEarlyLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")

	// An early line whose JSON-encoded size is over the OLD 1 MiB ceiling but
	// comfortably under the NEW 16 MiB one -- e.g. a large tool_result pasted
	// into the transcript.
	oversized := struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}{
		Type:    "user",
		Content: strings.Repeat("a", 2<<20), // 2 MiB payload
	}
	oversizedLine, err := json.Marshal(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if len(oversizedLine) <= 1<<20 {
		t.Fatalf("oversized line is only %d bytes, want > 1<<20 (the old ceiling)", len(oversizedLine))
	}
	if len(oversizedLine) >= 16<<20 {
		t.Fatalf("oversized line is %d bytes, want < 16<<20 (the new ceiling)", len(oversizedLine))
	}

	const wantTitle = "big-early-line-session"
	body := string(oversizedLine) + "\n" +
		`{"type":"custom-title","customTitle":"` + wantTitle + `","sessionId":"x"}` + "\n"

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if !transcriptHasTitle(path, wantTitle) {
		t.Errorf("transcriptHasTitle(%q, %q) = false, want true (a custom-title record follows an oversized early line -- past the old 1 MiB ceiling but within the new 16 MiB one)", path, wantTitle)
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
