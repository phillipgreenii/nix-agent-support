package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Synthetic session ids. They are random-looking lower-case UUIDs with no
// meaning outside these tests.
const (
	liveSession     = "11111111-1111-4111-8111-111111111111"
	strandedSession = "22222222-2222-4222-8222-222222222222"
)

// writeTranscript writes one synthetic transcript line per message under
// <claudeDir>/projects/<slug>/<name> and stamps the file's mtime. The shapes
// follow the user/assistant event lines Claude Code writes; every line is built
// with json.Marshal so quoting is whatever a real writer would produce.
func writeTranscript(t *testing.T, claudeDir, slug, name string, mtime time.Time, texts ...string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for i, text := range texts {
		line, err := json.Marshal(map[string]any{
			"type":      "user",
			"uuid":      "event-" + name + "-" + string(rune('a'+i)),
			"timestamp": mtime.UTC().Format(time.RFC3339),
			"sessionId": "synthetic",
			"message":   map[string]any{"role": "user", "content": text},
		})
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// strandedClaudeDir builds a claude directory in which only liveSession has a
// recently written transcript.
func strandedClaudeDir(t *testing.T, now time.Time) string {
	t.Helper()
	dir := t.TempDir()
	writeTranscript(t, dir, "-synthetic-slug", liveSession+".jsonl", now.Add(-time.Hour), "hello")
	return dir
}
