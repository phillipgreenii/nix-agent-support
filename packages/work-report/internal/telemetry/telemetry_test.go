package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
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

func sampleReport() ReportLog {
	preset := "weekly"
	since := "2026-10-12T00:00:00Z"
	dir := "/out/weekly/20261020T012800Z-ab12"
	zero := 0
	return ReportLog{
		TS: ts, RunID: "20261020T012800Z-ab12", Preset: &preset, Generator: "narrative",
		Status: "ok", OutcomeKind: "ok", GeneratorExit: &zero,
		Range:      ReportRange{Spec: "last-week", Since: &since, Before: "2026-10-19T00:00:00Z"},
		Narrowing:  ReportNarrowing{Labels: []string{"x"}},
		Overrides:  []string{"--label"},
		EntryCount: 4, FileCount: 1, OutputDir: &dir, ToolVersion: "1.2.3", GeneratorMS: 7, TotalMS: 9,
	}
}

// The report line carries exactly the design's key set: the retired kind,
// since, before and duration_ms keys and the rendered/outcome vocabulary are
// gone, and stderr_tail appears only on a failed run.
func TestLogReportHasExactlyTheDesignKeys(t *testing.T) {
	dir := t.TempDir()
	if err := New(dir).LogReport(sampleReport()); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "log", "report.jsonl"))
	want := []string{
		"entry_count", "file_count", "generator", "generator_exit", "generator_ms", "narrowing", "outcome_kind",
		"output_dir", "overrides", "preset", "range", "reason", "run_id", "status", "tool_version", "total_ms", "ts",
	}
	if len(lines) != 1 || !reflect.DeepEqual(keys(lines[0]), want) {
		t.Fatalf("keys = %v, want %v", keys(lines[0]), want)
	}
	for _, retired := range []string{"kind", "since", "before", "duration_ms"} {
		if _, ok := lines[0][retired]; ok {
			t.Errorf("retired key %q is still written", retired)
		}
	}
	m := lines[0]
	rng := m["range"].(map[string]any)
	if m["status"] != "ok" || m["preset"] != "weekly" || rng["spec"] != "last-week" || rng["since"] != "2026-10-12T00:00:00Z" ||
		m["generator_exit"].(float64) != 0 || m["file_count"].(float64) != 1 || m["output_dir"] != "/out/weekly/20261020T012800Z-ab12" {
		t.Errorf("unexpected values: %v", m)
	}
	if !reflect.DeepEqual(keys(rng), []string{"before", "since", "spec"}) {
		t.Errorf("range keys = %v", keys(rng))
	}
	nar := m["narrowing"].(map[string]any)
	if !reflect.DeepEqual(nar["sources"], []any{}) || !reflect.DeepEqual(nar["labels"], []any{"x"}) {
		t.Errorf("narrowing = %v, want empty lists written as []", nar)
	}
	if _, err := os.Stat(filepath.Join(dir, "log", "pull.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("pull.jsonl should not exist after a report write: %v", err)
	}
}

// A run that failed before a generator started still writes a line; its null
// fields are written as null, not omitted, and a failed run adds stderr_tail.
func TestLogReportFailedRunAddsStderrTail(t *testing.T) {
	dir := t.TempDir()
	tail := "boom"
	r := ReportLog{TS: ts, RunID: "r", Generator: "nosuch", Status: "failed", OutcomeKind: "not_found", Reason: "no such generator", StderrTail: &tail}
	if err := New(dir).LogReport(r); err != nil {
		t.Fatal(err)
	}
	m := readLines(t, filepath.Join(dir, "log", "report.jsonl"))[0]
	for _, k := range []string{"preset", "generator_exit", "output_dir"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
	if m["stderr_tail"] != "boom" || m["outcome_kind"] != "not_found" {
		t.Errorf("line = %v", m)
	}
}

// The report log and its directory are private: stderr is untrusted text.
func TestReportLogModes(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A report.jsonl left by an older version, world-readable.
	if err := os.WriteFile(filepath.Join(logDir, "report.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).LogReport(sampleReport()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{logDir: 0o700, filepath.Join(logDir, "report.jsonl"): 0o600} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}

	// A fresh log is created 0600 whatever the umask.
	fresh := t.TempDir()
	if err := New(fresh).LogReport(sampleReport()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(fresh, "log", "report.jsonl"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("fresh report.jsonl mode = %v, %v; want 0600", st.Mode(), err)
	}
}

// Concurrent writers (the scheduled pull and several report runs) never
// corrupt report.jsonl: every line stays one complete JSON object. Each writer
// has its own Logger, so the in-process mutex does not help.
func TestLogReportConcurrentWritersDoNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 25
	big := strings.Repeat("x", StderrTailKeep)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			l := New(dir)
			for i := 0; i < each; i++ {
				r := sampleReport()
				r.Status, r.OutcomeKind, r.StderrTail = "failed", "exit_nonzero", &big
				r.RunID = fmt.Sprintf("w%d-%d", w, i)
				if err := l.LogReport(r); err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	lines := readLines(t, filepath.Join(dir, "log", "report.jsonl"))
	if len(lines) != writers*each {
		t.Fatalf("got %d intact lines, want %d", len(lines), writers*each)
	}
	seen := map[string]bool{}
	for _, m := range lines {
		seen[m["run_id"].(string)] = true
	}
	if len(seen) != writers*each {
		t.Errorf("%d distinct run ids, want %d", len(seen), writers*each)
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
	if err := New("").LogReport(ReportLog{TS: ts, Generator: "baseline", Status: "failed", Reason: "none"}); err != nil {
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
