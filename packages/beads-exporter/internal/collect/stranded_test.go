package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/logx"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// strandedAdapter returns an adapter holding two claims: one whose session has
// a fresh transcript and one that has none.
func strandedAdapter(clock *fakeClock) *fakeAdapter {
	created := clock.Now().Add(-72 * time.Hour)
	live := mkBead("alpha-1", "in_progress", "task", 1, created)
	live.Assignee = liveSession + "-drain"
	live.StartedAt = ptr(clock.Now().Add(-10 * time.Hour))
	gone := mkBead("alpha-2", "open", "task", 2, created)
	gone.Assignee = strandedSession + "-drain"
	gone.UpdatedAt = ptr(clock.Now().Add(-30 * time.Hour))
	return &fakeAdapter{list: []bd.Bead{live, gone}}
}

func newStrandedCollector(t *testing.T, clock *fakeClock, ad *fakeAdapter, claudeDir string, log *slog.Logger) *Collector {
	t.Helper()
	sp := NewStrandedPass(StrandedConfig{ClaudeDir: claudeDir, Window: 6 * time.Hour}, log)
	return New(clock, []DB{{Name: "alpha", Adapter: ad}}, []Pass{sp}, quietLogger())
}

func TestStrandedPassEmitsCountsTimestampAndOneLogLinePerStrandedClaim(t *testing.T) {
	clock := newClock()
	ad := strandedAdapter(clock)
	dir := strandedClaudeDir(t, clock.Now())
	// A transcript line that mentions the stranded claim only in prose: the
	// marker text must never reach the log.
	writeTranscript(t, dir, "-synthetic-slug", "other.jsonl", clock.Now().Add(-time.Hour), "TRANSCRIPT-TEXT-MARKER "+strandedSession)
	var logs bytes.Buffer
	c := newStrandedCollector(t, clock, ad, dir, logx.New(&logs, slog.LevelInfo))

	c.RunPass(context.Background(), PassStranded)

	if got := ad.callNames(); !reflect.DeepEqual(got, []string{"list"}) {
		t.Fatalf("stranded pass made calls %v, want exactly one guarded list", got)
	}
	fams := samplesByFamily(c)
	for status, want := range map[string]float64{"open": 1, "in_progress": 0, "hooked": 0} {
		if v, ok := valueOf(t, fams[metrics.FamStrandedClaims], "db", "alpha", "status", status); !ok || v != want {
			t.Fatalf("stranded{%s} = %v %v, want %v", status, v, ok, want)
		}
	}
	oldest := clock.Now().Add(-30 * time.Hour).Unix()
	if v, ok := valueOf(t, fams[metrics.FamOldestStranded], "db", "alpha"); !ok || v != float64(oldest) {
		t.Fatalf("oldest stranded = %v %v, want %d", v, ok, oldest)
	}
	if v, ok := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "stranded"); !ok || v != float64(clock.Now().UnixNano())/1e9 {
		t.Fatalf("pass last success = %v %v", v, ok)
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d log lines, want one per stranded claim: %s", len(lines), logs.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	wantRec := map[string]any{
		"event": "stranded_claim", "bead": "alpha-2", "db": "alpha",
		"assignee": strandedSession + "-drain", "status": "open",
		"claim_time": clock.Now().Add(-30 * time.Hour).UTC().Format(time.RFC3339),
		"service":    "beads-exporter", "level": "info",
	}
	for k, v := range wantRec {
		if rec[k] != v {
			t.Fatalf("log field %s = %v, want %v (record %v)", k, rec[k], v, rec)
		}
	}
	if strings.Contains(logs.String(), "TRANSCRIPT-TEXT-MARKER") {
		t.Fatalf("transcript text leaked into the log: %s", logs.String())
	}
}

func TestStrandedLogLineOmitsAnUnknownClaimTime(t *testing.T) {
	clock := newClock()
	bare := mkBead("alpha-9", "open", "task", 1, clock.Now().Add(-time.Hour))
	bare.Assignee = "worker-gamma"
	ad := &fakeAdapter{list: []bd.Bead{bare}}
	var logs bytes.Buffer
	c := newStrandedCollector(t, clock, ad, strandedClaudeDir(t, clock.Now()), logx.New(&logs, slog.LevelInfo))
	c.RunPass(context.Background(), PassStranded)
	if strings.Contains(logs.String(), "claim_time") || !strings.Contains(logs.String(), "alpha-9") {
		t.Fatalf("log = %s", logs.String())
	}
	if _, ok := valueOf(t, samplesByFamily(c)[metrics.FamOldestStranded], "db", "alpha"); ok {
		t.Fatal("oldest series emitted for a claim with no known time")
	}
}

func TestStrandedFailureCountsEnumReasonDropsSeriesFreezesTimestampThenRecovers(t *testing.T) {
	clock := newClock()
	ad := strandedAdapter(clock)
	dir := strandedClaudeDir(t, clock.Now())
	c := newStrandedCollector(t, clock, ad, dir, quietLogger())

	c.RunPass(context.Background(), PassStranded)
	okAt := clock.Now()
	if len(samplesByFamily(c)[metrics.FamStrandedClaims]) == 0 {
		t.Fatal("no stranded series after a good pass")
	}

	// Transcript-side failure: the projects tree disappears.
	clock.Advance(time.Minute)
	projects := filepath.Join(dir, "projects")
	moved := projects + ".away"
	if err := os.Rename(projects, moved); err != nil {
		t.Fatal(err)
	}
	c.RunPass(context.Background(), PassStranded)
	fams := samplesByFamily(c)
	if len(fams[metrics.FamStrandedClaims]) != 0 || len(fams[metrics.FamOldestStranded]) != 0 {
		t.Fatal("stranded series still served after a failed pass")
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "stranded", "reason", "transcript_error"); v != 1 {
		t.Fatalf("transcript_error counter = %v, want 1", v)
	}
	if v, ok := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "stranded", "reason", "bd_error"); !ok || v != 0 {
		t.Fatalf("bd_error counter = %v %v, want a zero-filled 0", v, ok)
	}
	if v, _ := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "stranded"); v != float64(okAt.UnixNano())/1e9 {
		t.Fatalf("pass last success = %v, want it frozen at %v", v, okAt)
	}

	// A bd-side failure is classified by its own enum reason.
	clock.Advance(time.Minute)
	ad.fail = map[string]error{"list": failure.New(failure.StaleIssuesJSONL, "guard", errf("issues.jsonl exists"))}
	c.RunPass(context.Background(), PassStranded)
	fams = samplesByFamily(c)
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "stranded", "reason", "stale_issues_jsonl"); v != 1 {
		t.Fatalf("stale_issues_jsonl counter = %v, want 1", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "stranded", "reason", "transcript_error"); v != 1 {
		t.Fatalf("transcript_error counter = %v, want it to stay 1", v)
	}

	// Recovery: the timestamp advances again and the series return.
	clock.Advance(time.Minute)
	ad.fail = nil
	if err := os.Rename(moved, projects); err != nil {
		t.Fatal(err)
	}
	c.RunPass(context.Background(), PassStranded)
	fams = samplesByFamily(c)
	if v, _ := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "stranded"); v != float64(clock.Now().UnixNano())/1e9 {
		t.Fatalf("pass last success after recovery = %v", v)
	}
	if v, ok := valueOf(t, fams[metrics.FamStrandedClaims], "db", "alpha", "status", "open"); !ok || v != 1 {
		t.Fatalf("stranded{open} after recovery = %v %v", v, ok)
	}
}

func TestStrandedFailureNeverTouchesAnotherDBOrPass(t *testing.T) {
	clock := newClock()
	good := strandedAdapter(clock)
	bad := strandedAdapter(clock)
	bad.fail = map[string]error{"list": failure.New(failure.Timeout, "list", errf("slow"))}
	sp := NewStrandedPass(StrandedConfig{ClaudeDir: strandedClaudeDir(t, clock.Now()), Window: 6 * time.Hour}, quietLogger())
	mp := NewMainPass(nil, 5)
	good.statuses = []string{"open", "in_progress", "closed"}
	good.counts = map[string]int{}
	bad.statuses = good.statuses
	bad.counts = map[string]int{}
	dbs := []DB{{Name: "alpha", Adapter: good}, {Name: "beta", Adapter: bad}}
	c := New(clock, dbs, []Pass{mp, sp}, quietLogger())
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	c.RunPass(context.Background(), PassStranded)

	fams := samplesByFamily(c)
	if _, ok := valueOf(t, fams[metrics.FamStrandedClaims], "db", "alpha", "status", "open"); !ok {
		t.Fatal("the healthy db lost its stranded series")
	}
	if _, ok := valueOf(t, fams[metrics.FamStrandedClaims], "db", "beta", "status", "open"); ok {
		t.Fatal("the failing db still has stranded series")
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "beta", "pass", "stranded", "reason", "timeout"); v != 1 {
		t.Fatalf("timeout counter = %v", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamExporterUp], "db", "beta"); v != 0 {
		// The main pass of beta also timed out (same scripted failure), but
		// that is the main pass's own bookkeeping.
		t.Fatalf("beta up = %v", v)
	}
	if _, ok := valueOf(t, fams[metrics.FamIssues], "db", "alpha", "state", "in_progress"); !ok {
		t.Fatal("the stranded pass disturbed the main pass series")
	}
}

func TestStrandedPassKeepsOneCachePerDatabase(t *testing.T) {
	sp := NewStrandedPass(StrandedConfig{ClaudeDir: t.TempDir(), Window: time.Hour}, quietLogger())
	a, b := sp.classifier("alpha"), sp.classifier("beta")
	if a == b || sp.classifier("alpha") != a {
		t.Fatal("classifiers must be one per database and reused across passes")
	}
}

func TestStrandedPassIdentityAndReasons(t *testing.T) {
	sp := NewStrandedPass(StrandedConfig{}, quietLogger())
	if sp.Name() != "stranded" {
		t.Fatalf("name = %q", sp.Name())
	}
	want := []failure.Reason{
		failure.StaleIssuesJSONL, failure.Timeout, failure.BDError, failure.SchemaSkew, failure.ParseError, failure.TranscriptError,
	}
	if !reflect.DeepEqual(sp.Reasons(), want) {
		t.Fatalf("reasons = %v, want %v", sp.Reasons(), want)
	}
}

func TestStrandedPassNeverCallsAnythingButList(t *testing.T) {
	clock := newClock()
	ad := strandedAdapter(clock)
	c := newStrandedCollector(t, clock, ad, strandedClaudeDir(t, clock.Now()), quietLogger())
	c.RunPass(context.Background(), PassStranded)
	c.RunPass(context.Background(), PassStranded)
	for _, name := range ad.callNames() {
		if name != "list" {
			t.Fatalf("the stranded pass called %q: it must only list (read-only)", name)
		}
	}
	if opts := ad.listOpts; len(opts) != 2 || opts[0] != (bd.ListOpts{}) {
		t.Fatalf("list options = %+v, want the default not-closed view", opts)
	}
}
