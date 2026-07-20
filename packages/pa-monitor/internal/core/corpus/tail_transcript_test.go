package corpus

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// fakeRecorder is a local corpus-package PhaseRecorder double (test files are not
// importable across packages, so this mirrors poller_test.go's fakeRec). It
// counts calls per label and ignores durations (wall-clock, non-deterministic).
type fakeRecorder struct {
	scans  map[string]int
	phases map[string]int
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{scans: map[string]int{}, phases: map[string]int{}}
}
func (f *fakeRecorder) RecordScan(mode string, _ time.Duration, _ int64) { f.scans[mode]++ }
func (f *fakeRecorder) RecordPhase(phase string, _ time.Duration)        { f.phases[phase]++ }

func assistantLine(model string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}`, model, in, out)
}

func userPromptLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, text)
}

func appendLines(t *testing.T, path string, mtime time.Time, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append %s: %v", path, err)
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	_ = f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestTranscriptTail_IncrementalEqualsCold(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "s.jsonl", time.Unix(1000, 0),
		userPromptLine("hello"), assistantLine("claude-x", 100, 50))

	warm := newTranscriptTail()
	rec := newFakeRecorder()
	_, _, _ = warm.fold(path, time.Unix(1000, 0), rec) // full
	appendLines(t, path, time.Unix(2000, 0), assistantLine("claude-x", 200, 30))
	gotIncremental, incRecs, _ := warm.fold(path, time.Unix(2000, 0), rec) // incremental

	cold := newTranscriptTail()
	gotCold, coldRecs, _ := cold.fold(path, time.Unix(2000, 0), newFakeRecorder()) // full parse of whole file

	if !reflect.DeepEqual(gotIncremental, gotCold) {
		t.Fatalf("incremental != cold:\n inc=%+v\ncold=%+v", gotIncremental, gotCold)
	}
	if !reflect.DeepEqual(incRecs, coldRecs) {
		t.Fatalf("incremental records != cold records:\n inc=%+v\ncold=%+v", incRecs, coldRecs)
	}
	if gotCold.TotalTokens != 80 || gotCold.Model != "claude-x" || gotCold.FirstPrompt != "hello" {
		t.Fatalf("unexpected snapshot: %+v", gotCold)
	}
	if len(coldRecs) != 2 {
		t.Fatalf("cold records len = %d, want 2 (two assistant usage lines)", len(coldRecs))
	}
}

func TestTranscriptTail_CacheHitOnUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "s.jsonl", time.Unix(1000, 0), assistantLine("claude-x", 100, 50))
	tt := newTranscriptTail()
	rec := newFakeRecorder()

	snap1, recs1, _ := tt.fold(path, time.Unix(1000, 0), rec)
	scansAfterFirst := tt.scans
	snap2, recs2, _ := tt.fold(path, time.Unix(1000, 0), rec) // same path+mtime -> cache hit

	if !reflect.DeepEqual(snap1, snap2) {
		t.Fatalf("cache-hit snapshot differs: %+v vs %+v", snap1, snap2)
	}
	// SHIP-BLOCKER guard: records MUST survive a cache-hit tick (else the dominant
	// unchanged-file tick returns nil and setRecords wipes the pricing corpus).
	if !reflect.DeepEqual(recs1, recs2) {
		t.Fatalf("cache-hit records differ: %+v vs %+v (records must be cached)", recs1, recs2)
	}
	if len(recs2) != 1 {
		t.Fatalf("cache-hit records len = %d, want 1", len(recs2))
	}
	if tt.scans != scansAfterFirst {
		t.Fatalf("ScanIncremental re-invoked on cache hit: scans %d -> %d", scansAfterFirst, tt.scans)
	}
}

func TestTranscriptTail_RecordScanModes(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "s.jsonl", time.Unix(1000, 0), assistantLine("claude-x", 100, 50))
	tt := newTranscriptTail()
	rec := newFakeRecorder()

	_, _, _ = tt.fold(path, time.Unix(1000, 0), rec) // full
	_, _, _ = tt.fold(path, time.Unix(1000, 0), rec) // cache_hit
	appendLines(t, path, time.Unix(2000, 0), assistantLine("claude-x", 10, 5))
	_, _, _ = tt.fold(path, time.Unix(2000, 0), rec) // incremental

	if rec.scans["full"] != 1 || rec.scans["cache_hit"] != 1 || rec.scans["incremental"] != 1 {
		t.Fatalf("RecordScan modes = %v, want full=1 cache_hit=1 incremental=1", rec.scans)
	}
}

func TestTranscriptTail_EmptyPathRecordsFull(t *testing.T) {
	tt := newTranscriptTail()
	rec := newFakeRecorder()
	snap, recs, err := tt.fold("", time.Time{}, rec)
	if err != nil {
		t.Fatalf("empty-path fold err = %v, want nil", err)
	}
	if !reflect.DeepEqual(snap, transcript.Snapshot{}) {
		t.Fatalf("empty-path fold returned non-zero snapshot: %+v", snap)
	}
	if recs != nil {
		t.Fatalf("empty-path fold records = %+v, want nil", recs)
	}
	if rec.scans["full"] != 1 {
		t.Fatalf("empty-path fold RecordScan = %v, want full=1 (parity with poller.go:208-210)", rec.scans)
	}
}

// TestTranscriptTail_PruneByPath proves the tail is keyed by path and prune drops
// entries whose path is absent from the active set.
func TestTranscriptTail_PruneByPath(t *testing.T) {
	dir := t.TempDir()
	pathA := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), assistantLine("m", 1, 2))
	pathB := writeTranscript(t, dir, "b.jsonl", time.Unix(1000, 0), assistantLine("m", 3, 4))
	tt := newTranscriptTail()
	rec := newFakeRecorder()
	_, _, _ = tt.fold(pathA, time.Unix(1000, 0), rec)
	_, _, _ = tt.fold(pathB, time.Unix(1000, 0), rec)
	if len(tt.cache) != 2 {
		t.Fatalf("cache has %d entries, want 2", len(tt.cache))
	}
	tt.prune(map[string]bool{pathA: true}) // only A active
	if len(tt.cache) != 1 {
		t.Fatalf("after prune cache has %d entries, want 1 (B dropped)", len(tt.cache))
	}
	if _, ok := tt.cache[pathA]; !ok {
		t.Fatalf("prune dropped the active path A")
	}
}

func TestSessionSnapshotObserver_SetGetPrune(t *testing.T) {
	o := NewSessionSnapshotObserver()
	dir := t.TempDir()
	path := writeTranscript(t, dir, "s.jsonl", time.Unix(1000, 0), assistantLine("claude-x", 100, 50))
	snap, _, _ := newTranscriptTail().fold(path, time.Unix(1000, 0), newFakeRecorder())
	o.set("s", snap)

	got, ok := o.Snapshot("s")
	if !ok || got.TotalTokens != 50 {
		t.Fatalf("Snapshot(s) = (%+v, %v), want the stored snapshot", got, ok)
	}
	o.Prune(map[string]bool{}) // no active ids -> everything pruned
	if _, ok := o.Snapshot("s"); ok {
		t.Fatalf("Prune did not drop absent session")
	}
	crit := o.Criteria()
	if !classIn(crit.Classes, Transcript) || !crit.ActiveOnly {
		t.Fatalf("SessionSnapshot criteria = %+v, want {Transcript, ActiveOnly}", crit)
	}
}
