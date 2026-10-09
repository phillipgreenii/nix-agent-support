package stranded

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
)

// Synthetic ids. They are random-looking lower-case UUIDs with no meaning
// outside these tests.
const (
	sessA = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	sessB = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

const testWindow = 6 * time.Hour

// fixture is a synthetic Claude directory plus a classifier over it.
type fixture struct {
	t      *testing.T
	dir    string
	opener *countingOpener
	cl     *Classifier
}

func newFixture(t *testing.T, operators ...string) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), opener: &countingOpener{reads: map[string]int64{}}}
	f.cl = NewClassifier(Config{ClaudeDir: f.dir, Window: testWindow, OperatorNames: operators}, f.opener.Open)
	return f
}

// countingOpener records every path opened and every byte read, so a test can
// prove a file was never opened and only appended bytes were read.
type countingOpener struct {
	mu     sync.Mutex
	opened []string
	reads  map[string]int64
}

func (c *countingOpener) Open(path string) (File, error) {
	c.mu.Lock()
	c.opened = append(c.opened, path)
	c.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &countingFile{File: f, path: path, c: c}, nil
}

func (c *countingOpener) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened = nil
	c.reads = map[string]int64{}
}

func (c *countingOpener) openedPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.opened...)
}

func (c *countingOpener) bytesRead(path string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[path]
}

type countingFile struct {
	*os.File
	path string
	c    *countingOpener
}

func (f *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	f.c.mu.Lock()
	f.c.reads[f.path] += int64(n)
	f.c.mu.Unlock()
	return n, err
}

func (f *countingFile) Stat() (fs.FileInfo, error) { return f.File.Stat() }

// event builders: shapes follow the line types Claude Code writes, and every
// line is built with json.Marshal so quoting is exactly what a real writer
// produces.

// commandEvent is an assistant tool_use line running a shell command.
func commandEvent(command string) map[string]any {
	return map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "tool_use", "id": "tu", "name": "Bash",
				"input": map[string]any{"command": command},
			}},
		},
	}
}

// textEvent is a plain user line.
func textEvent(text string) map[string]any {
	return map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	}
}

// resultEvent is a user line carrying a tool result whose text is output.
func resultEvent(output string) map[string]any {
	return map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "tu", "content": output,
			}},
		},
	}
}

// rawEvent is a line carrying a structured tool result object, so JSON in it is
// not escaped.
func rawEvent(result any) map[string]any {
	return map[string]any{"type": "user", "toolUseResult": result}
}

func marshalLines(t *testing.T, events ...map[string]any) []byte {
	t.Helper()
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	return data
}

// write creates (or replaces) rel under <claudeDir>/projects and stamps its
// mtime to testNow-age.
func (f *fixture) write(rel string, age time.Duration, events ...map[string]any) string {
	f.t.Helper()
	path := filepath.Join(f.dir, "projects", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, marshalLines(f.t, events...), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.touch(path, age)
	return path
}

func (f *fixture) touch(path string, age time.Duration) {
	f.t.Helper()
	mt := testNow.Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) append(path string, age time.Duration, events ...map[string]any) {
	f.t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := fh.Write(marshalLines(f.t, events...)); err != nil {
		f.t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.touch(path, age)
}

func claimed(id, assignee, status string) bd.Bead {
	return bd.Bead{ID: id, Assignee: assignee, Status: status}
}

// classify runs the classifier at testNow and returns the stranded bead ids.
func (f *fixture) classify(beads ...bd.Bead) []string {
	f.t.Helper()
	res, err := f.cl.Classify(context.Background(), beads, testNow)
	if err != nil {
		f.t.Fatalf("Classify: %v", err)
	}
	ids := []string{}
	for _, c := range res.Claims {
		ids = append(ids, c.BeadID)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ptrTime(t time.Time) *time.Time { return &t }
