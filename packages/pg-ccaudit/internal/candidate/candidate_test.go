package candidate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The fixture is the committed mistake corpus, copied into t.TempDir() so no test
// can mutate it and none of them can reach the real transcript corpus or the real
// index. Both are shared with running sessions.
func extractFixture(t *testing.T, opt Options) Set {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	src := filepath.Join("..", "ingest", "testdata", "mistakes")
	if err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	dbPath := filepath.Join(base, "transcripts.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := ingest.Run(context.Background(), w, ingest.Options{
		Root: root, FinalAfter: ingest.FinalAfterImmediate, Progress: io.Discard,
	}); err != nil {
		t.Fatalf("ingest.Run: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("store.OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	set, err := Extract(context.Background(), db, opt)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return set
}

func TestExtractRunsEverySignalAndReportsItsProvenance(t *testing.T) {
	set := extractFixture(t, Options{})

	// Counts derive from the fixture table documented in
	// internal/query/mistake_test.go and are hand-computed, not copied from a run.
	want := map[Signal]int{
		TypedTurn:     3, // seq 6, 20 typed; 28 queued
		Interruption:  1, // seq 19
		Denial:        2, // seq 27 user-rejected; sess-s seq 5 permission-denied
		HookRejection: 1, // sess-m seq 31 carries a non-empty hookErrors payload
		Undo:          4, // edit-reversal, git-undo, write-then-delete, sidechain git-undo
		Churn:         1, // /w/a.txt, 5 edits
		EscapingRetry: 1, // seq 10 -> 12
		Ack:           4, // three Correction: stems and one ack phrase
	}
	got := map[Signal]int{}
	for _, s := range set.Sources {
		got[s.Signal] = s.Rows
		if s.Query == "" || s.Version == 0 {
			t.Errorf("signal %s has no query provenance — a candidate set must be as self-describing as a query result",
				s.Signal)
		}
	}
	for sig, n := range want {
		if got[sig] != n {
			t.Errorf("signal %s produced %d candidates, want %d", sig, got[sig], n)
		}
	}
	if len(set.Sources) != len(want) {
		t.Errorf("ran %d signals, want %d — a dropped detector is a silent recall loss", len(set.Sources), len(want))
	}
	total := 0
	for _, n := range want {
		total += n
	}
	if len(set.Candidates) != total {
		t.Errorf("got %d candidates, want %d", len(set.Candidates), total)
	}
}

func TestKeysAreUnique(t *testing.T) {
	// Measured on the real corpus: 63 gold entries matched 64 candidates because one
	// Edit reversed two earlier Edits, giving two rows at the same (path, seq). A
	// shared key silently attaches one row's verdict to the other.
	set := extractFixture(t, Options{})
	seen := map[string]bool{}
	for _, c := range set.Candidates {
		if c.Key == "" {
			t.Fatalf("candidate %s#%d has no key", c.Path, c.Seq)
		}
		if seen[c.Key] {
			t.Errorf("duplicate candidate key %q", c.Key)
		}
		seen[c.Key] = true
	}
}

func TestUniqueKeySuffixesARepeat(t *testing.T) {
	used := map[string]int{}
	if got := uniqueKey(used, "undo:a#4"); got != "undo:a#4" {
		t.Errorf("first use = %q, want the bare base", got)
	}
	if got := uniqueKey(used, "undo:a#4"); got != "undo:a#4~2" {
		t.Errorf("second use = %q, want the ordinal suffix", got)
	}
	if got := uniqueKey(used, "undo:a#4"); got != "undo:a#4~3" {
		t.Errorf("third use = %q", got)
	}
}

func TestOnlyAckIsSupplementary(t *testing.T) {
	set := extractFixture(t, Options{})
	for _, c := range set.Candidates {
		if (c.Signal == Ack) != c.Supplementary {
			// The constraint is in the type system precisely so acknowledgment text
			// cannot quietly become a primary detector: its vocabulary is shaped by the
			// harness system prompt and it only fires when the agent noticed and said so.
			t.Errorf("candidate %s: signal=%s supplementary=%t — exactly Ack must be supplementary",
				c.Key, c.Signal, c.Supplementary)
		}
	}
}

func TestSpanIsMeasuredOnlyBetweenAgentActions(t *testing.T) {
	set := extractFixture(t, Options{})
	for _, c := range set.Candidates {
		switch c.Signal {
		case TypedTurn, Interruption, Ack, Denial, HookRejection:
			// These intervals END at a human action (or have no interval at all), so a
			// span would be the person's reading-and-deciding time. Measured on the real
			// corpus that was 8,390,705,460 ms across 719 typed turns, and feeding it in
			// as "cost" promoted the noisiest signal to the top of the report.
			if c.SpanMS != 0 {
				t.Errorf("%s candidate %s has span %d — human latency is not agent waste",
					c.Signal, c.Key, c.SpanMS)
			}
			if c.StartTS != "" {
				t.Errorf("%s candidate %s carries StartTS; it must be left empty", c.Signal, c.Key)
			}
		case Undo, Churn, EscapingRetry:
			// Both endpoints are agent actions here, so the span IS agent time.
			if c.StartTS != "" && c.TS != "" && c.SpanMS <= 0 {
				t.Errorf("%s candidate %s has both timestamps but span %d", c.Signal, c.Key, c.SpanMS)
			}
		}
	}
}

func TestSignaturesGroupOccurrencesNotOccasions(t *testing.T) {
	set := extractFixture(t, Options{})
	byKind := map[string]string{}
	for _, c := range set.Candidates {
		if c.Signal != Undo {
			continue
		}
		if prev, ok := byKind[c.Kind]; ok && prev != c.Signature && c.Kind != "git-undo" {
			// write-then-delete and edit-reversal must collapse to one signature per
			// kind: the PATH is unique per occurrence, so grouping on it would make
			// every occurrence its own finding and no pattern would ever rank.
			t.Errorf("undo kind %s produced two signatures %q and %q", c.Kind, prev, c.Signature)
		}
		byKind[c.Kind] = c.Signature
	}
	if byKind["write-then-delete"] != "write-then-delete" {
		t.Errorf("write-then-delete signature = %q, want the bare kind", byKind["write-then-delete"])
	}
	// git-undo collapses through the ingest signature normalizer, so two resets to
	// different shas are one finding.
	for _, c := range set.Candidates {
		if c.Signal == Undo && c.Kind == "git-undo" && c.Signature == "git-undo: " {
			t.Errorf("git-undo signature is empty for %s", c.Key)
		}
	}
}

func TestEmptySignalsAreReported(t *testing.T) {
	// A signal filter that keeps only churn leaves every other detector at zero rows.
	// The point of the assertion is the REPORTING: the fixture deliberately exercises
	// hook-rejections, but on the real corpus that detector returns zero because
	// Claude Code writes only `hookErrors: []`, and a silent zero there reads as "no
	// hook rejected anything", which is false.
	set := extractFixture(t, Options{ChurnMin: 99})
	empty := set.EmptySignals()
	found := false
	for _, s := range empty {
		if s == Churn {
			found = true
		}
	}
	if !found {
		t.Errorf("churn was raised to 99 and produced nothing; EmptySignals()=%v must say so", empty)
	}
}

func TestOptionsOverrideTheQueryDefaults(t *testing.T) {
	low := extractFixture(t, Options{ChurnMin: 1})
	high := extractFixture(t, Options{ChurnMin: 6})
	count := func(s Set, sig Signal) int {
		n := 0
		for _, c := range s.Candidates {
			if c.Signal == sig {
				n++
			}
		}
		return n
	}
	if count(low, Churn) != 2 || count(high, Churn) != 0 {
		t.Errorf("churn at n=1 is %d and at n=6 is %d, want 2 and 0 — a silently inert threshold is worse than none",
			count(low, Churn), count(high, Churn))
	}
}

func TestSpanMSNeverGuesses(t *testing.T) {
	if got := spanMS("not a timestamp", "2026-08-01T00:00:00.000Z"); got != 0 {
		t.Errorf("spanMS with an unparseable start = %d, want 0 — a fabricated duration propagates into the ranking as if measured", got)
	}
	if got := spanMS("2026-08-01T00:00:02.000Z", "2026-08-01T00:00:01.000Z"); got != 0 {
		t.Errorf("a negative span = %d, want 0", got)
	}
	if got := spanMS("2026-08-01T00:00:01.000Z", "2026-08-01T00:00:03.500Z"); got != 2500 {
		t.Errorf("spanMS = %d, want 2500", got)
	}
}
