package usage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

type recSpec struct {
	ts    time.Time
	model string
	out   int
}

func writeTranscript(t *testing.T, path string, recs []recSpec) {
	t.Helper()
	var body []byte
	for _, r := range recs {
		line := `{"type":"assistant","timestamp":"` + r.ts.Format(time.RFC3339) +
			`","message":{"model":"` + r.model + `","usage":{"output_tokens":` + itoa(r.out) + `}}}` + "\n"
		body = append(body, line...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func mtimeOf(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

func setMtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func normRecs(recs []Record) []Record {
	out := make([]Record, len(recs))
	copy(out, recs)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Tokens.Output < out[j].Tokens.Output
	})
	return out
}

func recsEqual(t *testing.T, got, want []Record) {
	t.Helper()
	if !reflect.DeepEqual(normRecs(got), normRecs(want)) {
		t.Fatalf("record set mismatch:\n got=%v\nwant=%v", normRecs(got), normRecs(want))
	}
}

// buildHome writes two transcripts under home/projects and returns paths.
func buildHome(t *testing.T, base time.Time) (home, pA, pB string) {
	t.Helper()
	home = t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pA = filepath.Join(projDir, "a.jsonl")
	pB = filepath.Join(projDir, "b.jsonl")
	writeTranscript(t, pA, []recSpec{{base, "claude-opus-4-7", 1000000}})
	writeTranscript(t, pB, []recSpec{{base.Add(30 * time.Minute), "claude-opus-4-7", 500000}})
	return home, pA, pB
}

// The cached scan must return the identical record set to the uncached
// scanRecords oracle, across cold cache and every mutation shape.
func TestScanRecordsCached_EqualsUncachedOracle(t *testing.T) {
	base := time.Date(2026, 4, 23, 19, 0, 0, 0, time.UTC)
	home, pA, pB := buildHome(t, base)
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: func() time.Time { return base.Add(time.Hour) }}

	// cold cache
	got, _ := p.scanRecordsCached()
	want, _ := scanRecords(home)
	recsEqual(t, got, want)

	// warm cache, no change
	got, _ = p.scanRecordsCached()
	recsEqual(t, got, want)

	// append a record to A (mtime advances naturally)
	setMtime(t, pA, base) // pin old mtime first so the rewrite below changes it
	writeTranscript(t, pA, []recSpec{{base, "claude-opus-4-7", 1000000}, {base.Add(10 * time.Minute), "claude-sonnet-5", 2000}})
	got, _ = p.scanRecordsCached()
	want, _ = scanRecords(home)
	recsEqual(t, got, want)

	// add a new file
	pC := filepath.Join(filepath.Dir(pA), "c.jsonl")
	writeTranscript(t, pC, []recSpec{{base.Add(20 * time.Minute), "claude-opus-4-7", 300000}})
	got, _ = p.scanRecordsCached()
	want, _ = scanRecords(home)
	recsEqual(t, got, want)

	// rewrite B with different content
	writeTranscript(t, pB, []recSpec{{base.Add(40 * time.Minute), "claude-opus-4-7", 111}})
	got, _ = p.scanRecordsCached()
	want, _ = scanRecords(home)
	recsEqual(t, got, want)

	// remove C
	if err := os.Remove(pC); err != nil {
		t.Fatal(err)
	}
	got, _ = p.scanRecordsCached()
	want, _ = scanRecords(home)
	recsEqual(t, got, want)
}

// Proves the cache is actually consulted: after warming, we blank a file's
// content but FREEZE its mtime — a cache hit must return the old (cached)
// records (no re-read). Bumping the mtime then forces a re-read.
func TestScanRecordsCached_ReusesUnchangedByMtime(t *testing.T) {
	base := time.Date(2026, 4, 23, 19, 0, 0, 0, time.UTC)
	home, pA, _ := buildHome(t, base)
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: func() time.Time { return base.Add(time.Hour) }}

	warm, _ := p.scanRecordsCached()
	oldMtime := mtimeOf(t, pA)

	// Blank A on disk but restore its old mtime → cache must NOT re-read it.
	if err := os.WriteFile(pA, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	setMtime(t, pA, oldMtime)
	cached, _ := p.scanRecordsCached()
	recsEqual(t, cached, warm) // still has A's records → proves cache hit

	// Now advance A's mtime → cache invalidates, A re-read as empty.
	setMtime(t, pA, base.Add(2*time.Hour))
	after, _ := p.scanRecordsCached()
	want, _ := scanRecords(home) // A is empty on disk now
	recsEqual(t, after, want)
	if len(after) >= len(warm) {
		t.Fatalf("expected fewer records after A blanked+mtime-bumped: got %d, warm %d", len(after), len(warm))
	}
}

// ActiveBlock (which now uses the cache) must equal the uncached reference
// across mutations — the user-visible equivalence guarantee.
func TestActiveBlockCached_EqualsUncachedReference(t *testing.T) {
	base := time.Date(2026, 4, 23, 19, 0, 0, 0, time.UTC)
	now := base.Add(90 * time.Minute)
	home, pA, _ := buildHome(t, base)
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: func() time.Time { return now }}

	check := func(stage string) {
		got, err := p.ActiveBlock(context.Background())
		if err != nil {
			t.Fatalf("%s: ActiveBlock: %v", stage, err)
		}
		recs, _ := scanRecords(home)
		want := ActiveBlock(recs, stdPrices, now)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: cached ActiveBlock != uncached:\n got=%+v\nwant=%+v", stage, got, want)
		}
	}

	check("cold")
	check("warm")
	writeTranscript(t, pA, []recSpec{{base, "claude-opus-4-7", 1000000}, {base.Add(15 * time.Minute), "claude-opus-4-7", 250000}})
	check("after-append")
}
