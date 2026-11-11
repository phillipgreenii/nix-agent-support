package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

const cwd = "/example/proj"

var t0 = time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

func ts(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }

func userLine(sid string, at time.Duration, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"sessionId":%q,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`, ts(at), sid, text)
}

func assistantLine(sid string, at time.Duration, model string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"message":{"id":"m-%d","role":"assistant","model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}`, ts(at), sid, int(at/time.Second), model, in, out)
}

func titleLine(sid, title string) string {
	return fmt.Sprintf(`{"type":"custom-title","timestamp":%q,"customTitle":%q,"sessionId":%q}`, ts(0), title, sid)
}

func apiErrorLine(sid string, at time.Duration, kind, text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"error":%q,"isApiErrorMessage":true,"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":%q}]}}`, ts(at), sid, kind, text)
}

func statusLine(sid string, tsSec int64, pct float64, reset int64) string {
	return fmt.Sprintf(`{"ts":%d,"session_id":%q,"five_hour_pct":%v,"five_hour_resets_at":%d}`, tsSec, sid, pct, reset)
}

type rig struct {
	env *corpustest.Env
	m   *Monitor
	so  *SessionSnapshotObserver
	seo *SubagentErrorObserver
	po  *UsagePricingObserver
	lo  *LimitsObserver
}

func newRig(t *testing.T) *rig {
	t.Helper()
	e := corpustest.Start(t)
	m := New(e.Facts, e.Discovery())
	r := &rig{
		env: e, m: m, so: NewSessionSnapshotObserver(), seo: NewSubagentErrorObserver(),
		po: NewUsagePricingObserver(usage.PriceTable{Default: usage.ModelPrices{InputPerMTok: 5, OutputPerMTok: 25}}), lo: NewLimitsObserver(),
	}
	m.Register(r.so)
	m.Register(r.seo)
	m.Register(r.po)
	m.Register(r.lo)
	return r
}

func (r *rig) scan(t *testing.T) []*session.Session {
	t.Helper()
	s, err := r.m.Scan(t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return s
}

func TestScan_ResolvesByTitleAndFoldsTranscriptAndSubagents(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "Alpha", true)
	// The file named for the session id is NOT the one titled "Alpha": the title wins.
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "old"), assistantLine("s1", time.Second, "claude-x", 1, 1))
	r.env.Put(corpustest.TranscriptRel(cwd, "0b1c2d3e-0000-4000-8000-000000000005"), titleLine("0b1c2d3e-0000-4000-8000-000000000005", "Alpha"), userLine("0b1c2d3e-0000-4000-8000-000000000005", time.Minute, "hello"), assistantLine("0b1c2d3e-0000-4000-8000-000000000005", 2*time.Minute, "claude-x", 100, 50))
	r.env.Put(corpustest.SubagentRel(cwd, "0b1c2d3e-0000-4000-8000-000000000005", "a1"), apiErrorLine("0b1c2d3e-0000-4000-8000-000000000005", 3*time.Minute, "server_error", "boom"))

	sessions := r.scan(t)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	ref, mtime, ok := r.m.ResolvedPath("s1")
	if !ok || ref == "" {
		t.Fatalf("ResolvedPath not ok: %q %v", ref, ok)
	}
	if !mtime.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("mtime = %v, want the title-matched transcript's last record", mtime)
	}
	snap, _ := r.m.SessionSnapshot("s1")
	if snap.FirstPrompt != "hello" || snap.TotalTokens != 50 {
		t.Errorf("snapshot = %+v, want the Alpha transcript's fold", snap)
	}
	subErr, ok := r.m.SubagentError("s1")
	if !ok || subErr == nil || !subErr.FromSubagent || subErr.Text != "boom" {
		t.Fatalf("SubagentError = %+v %v, want the subagent's terminal error", subErr, ok)
	}
	if got := r.m.MaxActivity("s1"); !got.Equal(t0.Add(3 * time.Minute)) {
		t.Errorf("MaxActivity = %v, want the subagent's newer record time", got)
	}
}

func TestScan_FallsBackToSessionIDThenNewest(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s9"), userLine("s9", time.Hour, "newest"))
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "mine"))
	r.scan(t)
	if snap, _ := r.m.SessionSnapshot("s1"); snap.FirstPrompt != "mine" {
		t.Errorf("FirstPrompt = %q, want the session-id file even though another is newer", snap.FirstPrompt)
	}
}

func TestScan_IncrementalEqualsCold(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	rel := corpustest.TranscriptRel(cwd, "s1")
	r.env.Put(rel, userLine("s1", 0, "hello"), assistantLine("s1", time.Second, "claude-x", 100, 50))
	r.scan(t)
	r.env.Add(rel, assistantLine("s1", 2*time.Second, "claude-x", 200, 30))
	r.scan(t)
	warm, _ := r.m.SessionSnapshot("s1")

	cold := New(r.env.Facts, r.env.Discovery())
	so := NewSessionSnapshotObserver()
	cold.Register(so)
	if _, err := cold.Scan(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	coldSnap, _ := so.Snapshot("s1")
	if !reflect.DeepEqual(warm, coldSnap) {
		t.Fatalf("incremental != cold:\n warm=%+v\ncold=%+v", warm, coldSnap)
	}
	if warm.TotalTokens != 80 || warm.FirstPrompt != "hello" {
		t.Fatalf("unexpected snapshot %+v", warm)
	}
}

func TestScan_SteadyStateReadsNothing(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 3; i++ {
		sid := fmt.Sprintf("s%d", i)
		r.env.Register(100+i, sid, cwd, "", true)
		r.env.Put(corpustest.TranscriptRel(cwd, sid), userLine(sid, 0, "x"), assistantLine(sid, time.Second, "claude-x", 1, 1))
	}
	r.scan(t)
	if r.m.LinesFoldedLastScan() == 0 || r.m.FilesFoldedLastScan() != 3 {
		t.Fatalf("cold scan folded %d lines in %d files", r.m.LinesFoldedLastScan(), r.m.FilesFoldedLastScan())
	}
	r.scan(t)
	if r.m.LinesFoldedLastScan() != 0 || r.m.FilesFoldedLastScan() != 0 {
		t.Errorf("steady scan folded %d lines in %d files, want none", r.m.LinesFoldedLastScan(), r.m.FilesFoldedLastScan())
	}
	if got := r.m.QueriesLastScan(); got != 1 {
		t.Errorf("steady scan made %d recorder calls, want 1 (one feed poll)", got)
	}
}

func TestScan_AppliesResetBeforeFolding(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	rel := corpustest.TranscriptRel(cwd, "s1")
	r.env.Put(rel, userLine("s1", 0, "first draft"), assistantLine("s1", time.Second, "claude-x", 100, 900))
	r.scan(t)
	if snap, _ := r.m.SessionSnapshot("s1"); snap.TotalTokens != 900 {
		t.Fatalf("before rewrite: %+v", snap)
	}
	// Replace the file's bytes: the recorder starts a new generation and says so
	// with a reset entry; nothing of the old lines may survive in the fold.
	r.env.Put(rel, userLine("s1", 0, "second draft"), assistantLine("s1", time.Second, "claude-x", 1, 7))
	r.scan(t)
	snap, _ := r.m.SessionSnapshot("s1")
	if snap.FirstPrompt != "second draft" || snap.TotalTokens != 7 {
		t.Fatalf("after rewrite: %+v, want only the new bytes", snap)
	}
}

func TestScan_PricesTranscriptsOfNoSessionInsideTheWindowOnly(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "x"), assistantLine("s1", time.Second, "claude-x", 1000, 100))
	// A transcript of an ended-and-gone session, recent: priced. An ancient one: not read.
	r.env.Put(corpustest.TranscriptRel(cwd, "gone"), assistantLine("gone", 2*time.Second, "claude-x", 2000, 200))
	old := strings.ReplaceAll(assistantLine("old", 0, "claude-x", 9000, 900), ts(0), "2025-01-01T00:00:00Z")
	r.env.Put(corpustest.TranscriptRel(cwd, "old"), old)
	r.scan(t)
	if got, want := len(r.po.recs), 2; got != want {
		t.Errorf("pricing holds %d transcripts, want %d (session + recent, not ancient)", got, want)
	}
	if blk := r.m.Block(t0.Add(time.Hour)); blk == nil {
		t.Errorf("no active block from recent records")
	}
	if ok, err := r.m.CostProbed(); !ok || err != nil {
		t.Errorf("CostProbed = %v %v", ok, err)
	}
}

func TestScan_FoldsStatusFilesIntoLimits(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "x"))
	reset := t0.Add(4 * time.Hour).Unix()
	r.env.Put(corpustest.StatusRel(cwd, "s1"), statusLine("s1", t0.Unix(), 40, reset))
	r.scan(t)
	lim := r.m.Limits()
	if lim == nil || lim.FiveHourPct == nil || *lim.FiveHourPct != 40 {
		t.Fatalf("Limits = %+v, want 40%%", lim)
	}
	r.env.Add(corpustest.StatusRel(cwd, "s1"), statusLine("s1", t0.Unix()+60, 55, reset))
	r.scan(t)
	if lim := r.m.Limits(); lim == nil || *lim.FiveHourPct != 55 {
		t.Fatalf("Limits after append = %+v, want 55%%", lim)
	}
}

func TestScan_PicksUpASessionDiscoveredLater(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "one"))
	r.scan(t)
	r.env.Register(222, "s2", "/example/other", "", true)
	r.env.Put(corpustest.TranscriptRel("/example/other", "s2"), userLine("s2", 0, "two"))
	r.scan(t)
	if snap, _ := r.m.SessionSnapshot("s2"); snap.FirstPrompt != "two" {
		t.Errorf("s2 snapshot = %+v", snap)
	}
}

func TestScan_ReadsNoClaudeFile(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	rel := corpustest.TranscriptRel(cwd, "s1")
	r.env.Put(rel, userLine("s1", 0, "hello"), assistantLine("s1", time.Second, "claude-x", 10, 5))
	r.env.Unreadable()
	r.scan(t)
	if snap, _ := r.m.SessionSnapshot("s1"); snap.FirstPrompt != "hello" || snap.TotalTokens != 5 {
		t.Fatalf("snapshot without file access = %+v", snap)
	}
}

func TestScan_RecorderDownIsDaemonOfflineWithNoFallback(t *testing.T) {
	r := newRig(t)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "hello"))
	r.scan(t)
	r.env.Stop()
	_, err := r.m.Scan(t0.Add(time.Hour))
	if !errors.Is(err, session.ErrDaemonOffline) {
		t.Fatalf("Scan with the recorder down = %v, want daemon offline", err)
	}
}

// TestNoCodeOpensAClaudeFile pins that the corpus package never reads a
// transcript, subagent or status file itself: those calls are gone from it.
func TestNoCodeOpensAClaudeFile(t *testing.T) {
	banned := []string{"os.Open", "os.ReadFile", "os.ReadDir", "os.Stat", "os.Lstat", "TailLines", "ScanIncremental", "LastAPIError", "ReadStatusRecords"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range banned {
			if strings.Contains(string(b), call) {
				t.Errorf("%s calls %s: the corpus must read through the recorder", e.Name(), call)
			}
		}
	}
}

type dirtyRec struct{ dirty []int }

func (*dirtyRec) RecordScan(string, time.Duration, int64) {}
func (*dirtyRec) RecordPhase(string, time.Duration)       {}
func (d *dirtyRec) RecordScanDirtyFiles(n int)            { d.dirty = append(d.dirty, n) }

// Each Scan reports how many files are still dirty after its fold: the files a
// failed read left dirty, then 0 once a healthy scan converges.
func TestScan_ReportsDirtyFilesAfterEachFold(t *testing.T) {
	r := newRig(t)
	rec := &dirtyRec{}
	r.m.SetPhaseRecorder(rec)
	r.env.Register(111, "s1", cwd, "", true)
	r.env.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "hello"))
	r.scan(t) // bootstrap
	r.env.Add(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", time.Second, "more"))
	r.m.rd.readGate = func(*fileState) error { return context.DeadlineExceeded }
	r.scan(t)
	r.m.rd.readGate = nil
	r.scan(t)
	if len(rec.dirty) != 3 || rec.dirty[0] != 0 || rec.dirty[1] < 1 || rec.dirty[2] != 0 {
		t.Fatalf("dirty counts per scan = %v, want [0 >=1 0]", rec.dirty)
	}
}
