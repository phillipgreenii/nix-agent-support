package candidate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The fixture is the committed mistake corpus, copied into t.TempDir() so no test
// can mutate it and none of them can reach the real transcript corpus or the real
// index. Both are shared with running sessions.
func extractFixture(t *testing.T, opt Options) Set {
	t.Helper()
	return extractCorpus(t, "mistakes", opt)
}

// extractRefusalFixture runs the same extraction over pg2-v150u's corpus, which is
// separate for the reason internal/query/mistake_test.go records: its records would
// move every hand-computed answer in the mistakes fixture.
func extractRefusalFixture(t *testing.T, opt Options) Set {
	t.Helper()
	return extractCorpus(t, "refusals", opt)
}

func extractCorpus(t *testing.T, corpus string, opt Options) Set {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	src := filepath.Join("..", "ingest", "testdata", corpus)
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
		// 0 is CORRECT here and is asserted rather than omitted: this corpus contains no
		// hook-authored refusal body, so the mistakes fixture exercises the EMPTY path for
		// the new detector while internal/ingest/testdata/refusals exercises the full one.
		HookRefusalBody: 0,
		Undo:            4, // edit-reversal, git-undo, write-then-delete, sidechain git-undo
		Churn:           1, // /w/a.txt, 5 edits
		EscapingRetry:   1, // seq 10 -> 12
		Ack:             4, // three Correction: stems and one ack phrase
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
		case TypedTurn, Interruption, Ack, Denial, HookRejection, HookRefusalBody:
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

// TestHookRefusalBodyCandidates is the pg2-v150u detector at the candidate layer:
// the query's rows must arrive as typed candidates that GROUP, carry checkable
// evidence, and never claim a cost that was not measured.
func TestHookRefusalBodyCandidates(t *testing.T) {
	set := extractRefusalFixture(t, Options{})

	var got []Candidate
	for _, c := range set.Candidates {
		if c.Signal == HookRefusalBody {
			got = append(got, c)
		}
	}
	// Eight rows, hand-derived from the fixture table in
	// internal/query/mistake_test.go: seven in sess-r plus the one sidechain refusal.
	if len(got) != 8 {
		t.Fatalf("got %d hook-refusal-body candidates, want 8", len(got))
	}

	kinds := map[string]int{}
	for _, c := range got {
		kinds[c.Kind]++
		if c.Supplementary {
			t.Errorf("%s must NOT be supplementary: a refusal is a hook's recorded verdict, not the agent's own opinion of itself", c.Key)
		}
		if c.Excerpt == "" {
			t.Errorf("%s carries no excerpt — a refusal finding a reader cannot check is not evidence", c.Key)
		}
		if !strings.HasPrefix(c.Signature, "hook-refusal/") {
			t.Errorf("%s signature %q must be namespaced so it cannot collide with an error signature", c.Key, c.Signature)
		}
		if c.SpanMS != 0 || c.StartTS != "" {
			// The refusal lands on the tool_result line, so the only available span is
			// tool_use -> tool_result — and for a call that was REFUSED rather than run,
			// that is decision latency, not agent work. Feeding it in as cost is what put
			// 14 user rejections carrying 27.6 hours at the top of an earlier report.
			t.Errorf("%s claims a span (%d ms); a refused call's latency is not agent waste", c.Key, c.SpanMS)
		}
	}
	want := map[string]int{
		"blocked": 3, "must-include": 1, "refusing": 1,
		"prohibited": 1, "deny-listed": 1, "hook-error": 1,
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("kind %s produced %d candidates, want %d", k, kinds[k], n)
		}
	}
	if len(kinds) != len(want) {
		t.Errorf("got kinds %v, want exactly %v", kinds, want)
	}

	// One in a subagent — the split that decides whether the fix belongs in the
	// always-on rules or in the subagent brief.
	sub := 0
	for _, c := range got {
		if c.IsSidechain {
			sub++
		}
	}
	if sub != 1 {
		t.Errorf("%d subagent refusals, want 1", sub)
	}

	// The three `blocked` rows are three DIFFERENT guards (a .git write, a sleep poll,
	// a dd), so they must NOT collapse into one finding: the kind is a coarse bucket and
	// the normalized body is what separates the classes inside it.
	sigs := map[string]bool{}
	for _, c := range got {
		if c.Kind == "blocked" {
			sigs[c.Signature] = true
		}
	}
	if len(sigs) != 3 {
		t.Errorf("the three blocked refusals produced %d signatures, want 3 — a kind is not a finding", len(sigs))
	}
}

// TestHookRejectionStaysAnIndependentDetector is the bead's binding constraint,
// asserted at the layer that could quietly break it: pg2-v150u must NOT have turned
// the structured-payload reading into a fallback of the body reading.
func TestHookRejectionStaysAnIndependentDetector(t *testing.T) {
	mistakes := extractFixture(t, Options{})
	refusals := extractRefusalFixture(t, Options{})

	count := func(s Set, sig Signal) int {
		n := 0
		for _, src := range s.Sources {
			if src.Signal == sig {
				n = src.Rows
			}
		}
		return n
	}
	// The mistakes corpus has a hookErrors payload and no refusal bodies; the refusals
	// corpus is the exact opposite. Each detector fires on its own evidence and neither
	// suppresses the other.
	if count(mistakes, HookRejection) != 1 || count(mistakes, HookRefusalBody) != 0 {
		t.Errorf("mistakes corpus: hook-rejection=%d hook-refusal-body=%d, want 1 and 0",
			count(mistakes, HookRejection), count(mistakes, HookRefusalBody))
	}
	if count(refusals, HookRejection) != 0 || count(refusals, HookRefusalBody) != 8 {
		t.Errorf("refusals corpus: hook-rejection=%d hook-refusal-body=%d, want 0 and 8",
			count(refusals, HookRejection), count(refusals, HookRefusalBody))
	}
	// And the zero is REPORTED, not swallowed. That is the pg2-oisvb guarantee this bead
	// was forbidden from special-casing away: on the real corpus hook-rejection is the
	// empty one, and the day it stops being empty is the day the field started arriving.
	empty := map[Signal]bool{}
	for _, s := range refusals.EmptySignals() {
		empty[s] = true
	}
	if !empty[HookRejection] {
		t.Errorf("hook-rejection found nothing and EmptySignals()=%v does not say so", refusals.EmptySignals())
	}
	if empty[HookRefusalBody] {
		t.Errorf("hook-refusal-body found 8 rows and must not be reported empty")
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
