package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func keys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var ts = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestLogPullHasExactlyElevenKeys(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	err := l.LogPull(PullLog{TS: ts, Source: "src", Since: "2026-10-01T00:00:00Z", Status: "succeeded", Count: 3, Unchanged: 1, Rejected: 2, Truncated: true, DurationMS: 42})
	if err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "log", "pull.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	want := []string{"before", "count", "duration_ms", "reason", "rejected", "since", "source", "status", "truncated", "ts", "unchanged"}
	if got := keys(lines[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	m := lines[0]
	if m["ts"] != "2026-10-05T12:00:00Z" || m["source"] != "src" || m["before"] != "" || m["count"].(float64) != 3 || m["truncated"] != true || m["duration_ms"].(float64) != 42 {
		t.Fatalf("unexpected values: %v", m)
	}
}

func TestLogReportHasExpectedKeys(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	if err := l.LogReport(ReportLog{TS: ts, Kind: "baseline", Status: "rendered", DurationMS: 7}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "log", "report.jsonl"))
	want := []string{"before", "duration_ms", "kind", "reason", "since", "status", "ts"}
	if len(lines) != 1 || !reflect.DeepEqual(keys(lines[0]), want) {
		t.Fatalf("lines = %v, want keys %v", lines, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "log", "pull.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("pull.jsonl should not exist after a report write: %v", err)
	}
}

func TestAppendsNeverTruncates(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	for _, src := range []string{"a", "b"} {
		if err := l.LogPull(PullLog{TS: ts, Source: src, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh Logger over the same dir must also append.
	if err := New(dir).LogPull(PullLog{TS: ts, Source: "c", Status: "degraded", Reason: "boom"}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "log", "pull.jsonl"))
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"a", "b", "c"} {
		if lines[i]["source"] != want {
			t.Fatalf("line %d source = %v, want %s", i, lines[i]["source"], want)
		}
	}
}

func TestXDGStateHomeHonored(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if err := New("").LogPull(PullLog{TS: ts, Source: "s", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, filepath.Join(xdg, "work-report", "log", "pull.jsonl")); len(got) != 1 {
		t.Fatalf("want 1 line under XDG_STATE_HOME, got %d", len(got))
	}
}

func TestHomeFallbackWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if err := New("").LogReport(ReportLog{TS: ts, Kind: "baseline", Status: "outcome", Reason: "none"}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, filepath.Join(home, ".local", "state", "work-report", "log", "report.jsonl")); len(got) != 1 {
		t.Fatalf("want 1 line under HOME fallback, got %d", len(got))
	}
}

func TestLogDirCreatedOnFirstUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet")
	l := New(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("New must not create anything: %v", err)
	}
	if err := l.LogPull(PullLog{TS: ts, Source: "s", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "log")); err != nil || !st.IsDir() {
		t.Fatalf("log dir not created: %v", err)
	}
}

func TestWriteFailureIsReturned(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// stateDir is a regular file, so creating log/ beneath it must fail.
	if err := New(blocker).LogPull(PullLog{TS: ts}); err == nil {
		t.Fatal("expected an error when the log dir cannot be created")
	}
}
