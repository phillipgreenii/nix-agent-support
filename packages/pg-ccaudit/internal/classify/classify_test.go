package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/gold"
)

// Nothing in this suite reaches a model, a network or a credential: the CLI
// classifier's Runner is injected. That is a hard requirement rather than a
// convenience — a test that called the real classifier would cost money per run,
// return different answers on different days, and fail in the nix build sandbox,
// so it would be deleted or skipped within a week and the parsing path would go
// untested exactly where it is most fragile.

func cand(sig candidate.Signal, path string, seq int64, opts ...func(*candidate.Candidate)) candidate.Candidate {
	c := candidate.Candidate{
		Signal:    sig,
		Path:      path,
		Seq:       seq,
		Key:       fmt.Sprintf("%s:%s#%d", sig, path, seq),
		SessionID: "S1",
		Signature: string(sig) + " shape",
		Excerpt:   "evidence",
		Detail:    map[string]string{},
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func withPrevTool(seq string) func(*candidate.Candidate) {
	return func(c *candidate.Candidate) { c.Detail["prev_tool_seq"] = seq }
}

func TestBaselineImplementsTheNaiveRule(t *testing.T) {
	cands := []candidate.Candidate{
		cand(candidate.TypedTurn, "a.jsonl", 10, withPrevTool("8")), // follows a tool call
		cand(candidate.TypedTurn, "a.jsonl", 20),                    // opened the session
		cand(candidate.Undo, "a.jsonl", 30),                         // a different signal entirely
	}
	res, err := Baseline{}.Classify(context.Background(), cands)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []Class{ClassUserCorrection, ClassNotAMistake, ClassNotAMistake}
	for i, w := range want {
		if res.Classifications[i].Class != w {
			t.Errorf("candidate %d classified %s, want %s", i, res.Classifications[i].Class, w)
		}
	}
	// The baseline is the BAR, so it must cost nothing to re-run; a bar with a price
	// stops being re-checked.
	if res.Cost.Calls != 0 || res.Cost.USD != 0 {
		t.Errorf("baseline cost calls=%d usd=%f, want zero — it makes no model calls",
			res.Cost.Calls, res.Cost.USD)
	}
}

// fakeRunner answers with the classes it is handed, in the envelope shape
// `claude -p --output-format json` produces.
func fakeRunner(t *testing.T, classes map[string]string, usd float64, wrap func(string) string) (Runner, *int) {
	t.Helper()
	calls := 0
	return func(_ context.Context, _ []string, stdin string) ([]byte, error) {
		calls++
		// The prompt must carry every candidate's id, or a verdict cannot be attached
		// back to the row it judges.
		var verdicts []verdict
		for id, cl := range classes {
			if strings.Contains(stdin, id) {
				verdicts = append(verdicts, verdict{
					ID: id, Class: cl, Confidence: "high",
					What: "w", Prevention: "p", Route: "global-rule",
				})
			}
		}
		body, err := json.Marshal(verdicts)
		if err != nil {
			t.Fatalf("marshal fake verdicts: %v", err)
		}
		reply := string(body)
		if wrap != nil {
			reply = wrap(reply)
		}
		env := map[string]any{
			"result":         reply,
			"total_cost_usd": usd,
			"usage": map[string]any{
				"input_tokens":                10,
				"output_tokens":               20,
				"cache_read_input_tokens":     30,
				"cache_creation_input_tokens": 40,
			},
		}
		out, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal fake envelope: %v", err)
		}
		return out, nil
	}, &calls
}

func TestCLIBatchesAndReportsCost(t *testing.T) {
	var cands []candidate.Candidate
	classes := map[string]string{}
	for i := 0; i < 7; i++ {
		c := cand(candidate.TypedTurn, "a.jsonl", int64(i))
		cands = append(cands, c)
		classes[CandidateID(c)] = string(ClassNotAMistake)
	}
	run, calls := fakeRunner(t, classes, 0.05, nil)
	cl := &CLI{Command: []string{"fake"}, Batch: 3, Run: run}

	res, err := cl.Classify(context.Background(), cands)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// 7 candidates at 3 per call is 3 calls, not 7: the per-CALL overhead of priming
	// the harness system prompt (measured at 21,882 cache-creation tokens) is what
	// batching exists to amortise.
	if *calls != 3 || res.Cost.Batches != 3 || res.Cost.Calls != 3 {
		t.Errorf("calls=%d batches=%d, want 3 and 3", *calls, res.Cost.Batches)
	}
	if len(res.Classifications) != 7 {
		t.Errorf("got %d classifications, want 7", len(res.Classifications))
	}
	if diff := res.Cost.USD - 0.15; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("usd=%v, want 0.15 (three calls at 0.05) — the cost must ACCUMULATE across batches", res.Cost.USD)
	}
	if res.Cost.CacheCreationTokens != 120 {
		t.Errorf("cache_creation=%d, want 120", res.Cost.CacheCreationTokens)
	}
}

// TestCLIClassifyStreamingReportsEachBatchAsItCompletes is the streaming half
// of pg2-ohvpk: onBatch must fire once PER BATCH, with the cumulative cost as
// of that batch, rather than only once after the whole pass returns — that
// is what lets a caller persist and print a batch's results before paying
// for the next one.
func TestCLIClassifyStreamingReportsEachBatchAsItCompletes(t *testing.T) {
	var cands []candidate.Candidate
	classes := map[string]string{}
	for i := 0; i < 6; i++ {
		c := cand(candidate.TypedTurn, "a.jsonl", int64(i))
		cands = append(cands, c)
		classes[CandidateID(c)] = string(ClassNotAMistake)
	}
	run, calls := fakeRunner(t, classes, 0.02, nil)
	cl := &CLI{Command: []string{"fake"}, Batch: 2, Run: run}

	var seenBatches int
	var cumulativeCalls []int
	res, err := cl.ClassifyStreaming(context.Background(), cands, func(batch []Classification, cost Cost) error {
		seenBatches++
		cumulativeCalls = append(cumulativeCalls, cost.Calls)
		if len(batch) != 2 {
			t.Errorf("batch %d has %d classifications, want 2", seenBatches, len(batch))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ClassifyStreaming: %v", err)
	}
	if seenBatches != 3 {
		t.Fatalf("onBatch fired %d times, want 3 — one per batch, not once at the end", seenBatches)
	}
	for i, c := range cumulativeCalls {
		if c != i+1 {
			t.Errorf("onBatch %d saw cumulative calls=%d, want %d — cost must accumulate visibly BEFORE the pass finishes",
				i, c, i+1)
		}
	}
	if *calls != 3 || len(res.Classifications) != 6 {
		t.Errorf("calls=%d classifications=%d, want 3 and 6", *calls, len(res.Classifications))
	}
}

// TestCLIClassifyStreamingLeavesCompletedBatchesOnACancelMidRun is pg2-ohvpk's
// testable claim at the classifier layer: killing a run partway through must
// leave every batch that already completed intact — in the Result AND in
// everything onBatch already saw — because a caller has already made that
// much durable (cache entries, a ledger snapshot) by the time the
// cancellation is observed.
//
// The context is cancelled directly from inside onBatch rather than via a
// real SIGTERM to an OS process: main.go builds this exact ctx with
// signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM) and threads it
// down to this call unchanged, so cancelling it here is the precise
// in-process equivalent of that signal arriving — chosen because spawning
// the compiled binary and signalling it from `go test` is impractical in
// this sandbox, exactly the fallback pg2-ohvpk anticipates.
func TestCLIClassifyStreamingLeavesCompletedBatchesOnACancelMidRun(t *testing.T) {
	var cands []candidate.Candidate
	classes := map[string]string{}
	for i := 0; i < 6; i++ {
		c := cand(candidate.TypedTurn, "a.jsonl", int64(i))
		cands = append(cands, c)
		classes[CandidateID(c)] = string(ClassSelfCaught)
	}
	run, _ := fakeRunner(t, classes, 0.03, nil)
	cl := &CLI{Command: []string{"fake"}, Batch: 2, Run: run}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var streamed []Classification
	var seenBatches int
	res, err := cl.ClassifyStreaming(ctx, cands, func(batch []Classification, cost Cost) error {
		seenBatches++
		streamed = append(streamed, batch...)
		if seenBatches == 2 {
			// The SIGTERM-equivalent: as if it landed right after this
			// batch's results were already persisted by the caller.
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("a cancelled context must stop the pass with an error, not finish silently")
	}
	if len(streamed) != 4 {
		t.Fatalf("onBatch saw %d classifications before the cancel was observed, want 4 (2 completed batches)", len(streamed))
	}
	if len(res.Classifications) != 4 {
		t.Errorf("Result carries %d classifications, want 4 — the cancelled pass must not roll back completed batches",
			len(res.Classifications))
	}
	if res.Cost.Calls != 2 {
		t.Errorf("cost.Calls=%d, want 2 — the cost of the batches that actually ran must not be lost", res.Cost.Calls)
	}
}

func TestCLIToleratesAFencedReply(t *testing.T) {
	c := cand(candidate.Undo, "a.jsonl", 1)
	run, _ := fakeRunner(t, map[string]string{CandidateID(c): string(ClassSelfCaught)}, 0.01,
		func(s string) string { return "Here you go:\n```json\n" + s + "\n```\n" })
	cl := &CLI{Command: []string{"fake"}, Batch: 10, Run: run}
	res, err := cl.Classify(context.Background(), []candidate.Candidate{c})
	if err != nil {
		// Failing the batch over a pair of backticks would discard a call that was
		// already paid for.
		t.Fatalf("a fenced reply must still parse: %v", err)
	}
	if res.Classifications[0].Class != ClassSelfCaught {
		t.Errorf("class=%s, want %s", res.Classifications[0].Class, ClassSelfCaught)
	}
}

func TestCLIRejectsAnUnknownClass(t *testing.T) {
	c := cand(candidate.Undo, "a.jsonl", 1)
	run, _ := fakeRunner(t, map[string]string{CandidateID(c): "definitely-a-mistake"}, 0.01, nil)
	cl := &CLI{Command: []string{"fake"}, Batch: 10, Run: run}
	_, err := cl.Classify(context.Background(), []candidate.Candidate{c})
	if err == nil {
		// A silently coerced class would land in the per-class precision table as if
		// it had been measured.
		t.Fatal("an unknown class must fail loudly, not be coerced")
	}
}

func TestCLIDropsAVerdictForACandidateItWasNotSent(t *testing.T) {
	c := cand(candidate.Undo, "a.jsonl", 1)
	run, _ := fakeRunner(t, map[string]string{
		CandidateID(c): string(ClassSelfCaught),
	}, 0.01, func(s string) string {
		// Splice in a verdict for a candidate id that was never in the prompt.
		return strings.TrimSuffix(s, "]") +
			`,{"id":"undo:ghost.jsonl#99","class":"user-correction","confidence":"high"}]`
	})
	cl := &CLI{Command: []string{"fake"}, Batch: 10, Run: run}
	res, err := cl.Classify(context.Background(), []candidate.Candidate{c})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// Attaching it to the wrong row would corrupt precision and recall in a way
	// nothing downstream could detect.
	if len(res.Classifications) != 1 {
		t.Fatalf("got %d classifications, want 1 — a verdict for an unsent candidate must be dropped",
			len(res.Classifications))
	}
}

func TestPromptCarriesEveryCandidateIDAndTheSignalGuide(t *testing.T) {
	cands := []candidate.Candidate{
		cand(candidate.TypedTurn, "a.jsonl", 1),
		cand(candidate.Interruption, "a.jsonl", 2),
	}
	p, err := renderPrompt(cands)
	if err != nil {
		t.Fatalf("renderPrompt: %v", err)
	}
	for _, c := range cands {
		if !strings.Contains(p, CandidateID(c)) {
			t.Errorf("prompt is missing candidate id %s", CandidateID(c))
		}
	}
	// The rubric must keep not-a-mistake as the default and must keep the signal
	// guide: without the guide the classifier is asked to judge evidence whose
	// meaning it was never told (an interruption's excerpt is only the sentinel).
	for _, want := range []string{"not-a-mistake", "WHAT EACH SIGNAL DETECTED", "interruption", "prevention"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt no longer mentions %q", want)
		}
	}
	// The candidate SHAPE the model sees is narrower than candidate.Candidate: no
	// timestamps and no session id, because those invite reasoning about identity
	// rather than about evidence. The path survives only inside the id, which is
	// load-bearing for attaching the verdict back.
	if strings.Contains(p, "S1") {
		t.Error("the prompt must not carry the session id")
	}
}

// TestEveryPrimarySignalHasAGlossaryEntry is the guard the pg2-v150u detector needed
// and did not have: the rubric TELLS the model it needs the per-signal guide to read
// the evidence, so a detector shipped without an entry hands the classifier 160
// candidates whose meaning it was never given. That is a silent precision loss —
// the run still completes and still reports a number.
//
// hook-rejection is exempt because it is structurally unreachable: it returns zero
// rows corpus-wide (Claude Code writes `hookErrors: []`), so no candidate of that
// signal ever reaches a prompt. If it starts producing rows, the field began arriving
// and it needs an entry — which is exactly when this list should be revisited.
func TestEveryPrimarySignalHasAGlossaryEntry(t *testing.T) {
	exempt := map[candidate.Signal]bool{candidate.HookRejection: true}
	for _, sig := range []candidate.Signal{
		candidate.TypedTurn, candidate.Interruption, candidate.Denial,
		candidate.HookRejection, candidate.HookRefusalBody, candidate.Undo,
		candidate.Churn, candidate.EscapingRetry, candidate.Ack,
	} {
		if exempt[sig] {
			continue
		}
		if !strings.Contains(rubric, string(sig)) {
			t.Errorf("the rubric's signal guide has no entry for %q; the classifier would be judging evidence it was never told the meaning of", sig)
		}
	}
}

// TestEvaluateAgainstGold is criterion 4's machinery, on a hand-built confusion
// matrix so the arithmetic is checkable without a model.
func TestEvaluateAgainstGold(t *testing.T) {
	mk := func(sig candidate.Signal, seq int64) candidate.Candidate { return cand(sig, "a.jsonl", seq) }
	c1, c2, c3, c4 := mk(candidate.TypedTurn, 1), mk(candidate.TypedTurn, 2), mk(candidate.Undo, 3), mk(candidate.Undo, 4)

	g := gold.Set{Entries: []gold.Entry{
		{ID: CandidateID(c1), Source: gold.SourceHandLabelled, Class: string(ClassUserCorrection), Labeller: "op"},
		{ID: CandidateID(c2), Source: gold.SourceHandLabelled, Class: string(ClassNotAMistake), Labeller: "op"},
		{ID: CandidateID(c3), Source: gold.SourceHandLabelled, Class: string(ClassSelfCaught), Labeller: "op"},
		{ID: CandidateID(c4), Source: gold.SourceHandLabelled, Class: string(ClassUserCorrection), Labeller: "op"},
		{ID: "file:/x/FEEDBACK.md", Source: gold.SourceFileChannel, Class: string(ClassUserCorrection)},
	}}
	got := []Classification{
		{Candidate: c1, Class: ClassUserCorrection}, // TP for user-correction
		{Candidate: c2, Class: ClassUserCorrection}, // FP for user-correction
		{Candidate: c3, Class: ClassSelfCaught},     // TP for self-caught
		{Candidate: c4, Class: ClassNotAMistake},    // FN for user-correction
	}
	ev := Evaluate("test", g, got, []candidate.Candidate{c1, c2, c3, c4})

	if ev.Scored != 4 {
		t.Fatalf("scored=%d, want 4", ev.Scored)
	}
	// The file-channel entry is counted, never scored: there is no candidate to
	// classify, which is the whole point of the channel.
	if ev.FileChannel != 1 {
		t.Errorf("file_channel=%d, want 1", ev.FileChannel)
	}
	if !ev.UnderSized {
		t.Errorf("4 scored entries must be reported as under-sized against the floor of %d", MinGoldEntries)
	}
	// user-correction: TP 1, FP 1, FN 1 -> precision 0.5, recall 0.5.
	for _, m := range ev.PerClass {
		if m.Class != ClassUserCorrection {
			continue
		}
		if m.TP != 1 || m.FP != 1 || m.FN != 1 || m.Precision != 0.5 || m.Recall != 0.5 {
			t.Errorf("user-correction metrics = %+v, want TP/FP/FN 1/1/1 and 0.5/0.5", m)
		}
	}
	// The binary collapse is the axis the baseline can compete on: c3 is a mistake
	// in both gold and prediction but is NOT a correction, so it is a true negative.
	if ev.Correction.TP != 1 || ev.Correction.FP != 1 || ev.Correction.FN != 1 || ev.Correction.TN != 1 {
		t.Errorf("correction binary = %+v, want 1/1/1/1", ev.Correction)
	}
	if ev.Accuracy != 0.5 {
		t.Errorf("accuracy=%v, want 0.5 (2 of 4 exact)", ev.Accuracy)
	}
}

func TestCompareDecidesOnCorrectionF1NotAccuracy(t *testing.T) {
	// A classifier that answers not-a-mistake to everything scores well on accuracy
	// because that class is the majority, and finds nothing. Deciding criterion 4 on
	// accuracy would let it pass.
	lazy := Evaluation{Scored: 60, Accuracy: 0.9, Correction: Binary{Precision: 0, Recall: 0, F1: 0}}
	base := Evaluation{Scored: 60, Accuracy: 0.4, Correction: Binary{Precision: 0.14, Recall: 0.07, F1: 0.09}}
	if Compare(lazy, base).Beats {
		t.Error("a classifier with correction F1 0 must NOT beat the baseline on accuracy alone")
	}

	good := Evaluation{Scored: 60, Accuracy: 0.58, Correction: Binary{Precision: 1, Recall: 0.27, F1: 0.42}}
	c := Compare(good, base)
	if !c.Beats {
		t.Errorf("correction F1 0.42 must beat 0.09: %s", c.Reason)
	}
	if !strings.Contains(c.Reason, "F1") {
		t.Errorf("the verdict must state the numbers it was decided on: %q", c.Reason)
	}
}

func TestCompareRefusesToPassOnNothing(t *testing.T) {
	c := Compare(Evaluation{Scored: 0}, Evaluation{Scored: 0})
	if c.Beats {
		t.Error("zero scored entries must not pass criterion 4")
	}
	if !strings.Contains(c.Reason, "nothing was measured") {
		t.Errorf("reason must say nothing was measured, got %q", c.Reason)
	}
}

// TestMarkerRecallIsMeasuredNotAssumed covers the bead's binding requirement that
// the `Correction:` marker's COMPLIANCE be measured, because an unmeasured marker
// reintroduces the very drift it was added to remove.
func TestMarkerRecallIsMeasuredNotAssumed(t *testing.T) {
	mistake := cand(candidate.Undo, "a.jsonl", 100)
	unmarked := cand(candidate.Undo, "a.jsonl", 500)
	ack := cand(candidate.Ack, "a.jsonl", 110)
	ack.Kind = "correction-marker"
	ack.Supplementary = true
	farAck := cand(candidate.Ack, "a.jsonl", 900)
	farAck.Kind = "correction-marker"
	farAck.Supplementary = true

	got := []Classification{
		{Candidate: mistake, Class: ClassSelfCaught},
		{Candidate: unmarked, Class: ClassSelfCaught},
		{Candidate: ack, Class: ClassSelfCaught}, // supplementary: must not count as a mistake
	}
	ev := Evaluate("test", gold.Set{}, got, []candidate.Candidate{mistake, unmarked, ack, farAck})

	if ev.Marker.Mistakes != 2 {
		t.Errorf("marker denominator=%d, want 2 — a supplementary ack is corroboration, not a mistake of its own",
			ev.Marker.Mistakes)
	}
	if ev.Marker.MistakesWithAck != 1 {
		t.Errorf("marked=%d, want 1 (seq 110 is within %d lines of 100; seq 900 is not within %d of 500)",
			ev.Marker.MistakesWithAck, AckWindow, AckWindow)
	}
	if ev.Marker.Recall != 0.5 {
		t.Errorf("marker recall=%v, want 0.5", ev.Marker.Recall)
	}
	if ev.Marker.CorrectionStems != 2 {
		t.Errorf("correction stems=%d, want 2", ev.Marker.CorrectionStems)
	}
}

func TestRenderStatesTheMarkerCaveats(t *testing.T) {
	var sb strings.Builder
	Render(&sb, Compare(
		Evaluation{Scored: 60, Classifier: "x", Correction: Binary{F1: 0.4}},
		Evaluation{Scored: 60, Classifier: "baseline", Correction: Binary{F1: 0.1}},
	))
	out := sb.String()
	// These sentences are the guard against the report's own numbers being misread,
	// and they are the reason the bead insists the metric is an ACKNOWLEDGED mistake
	// rate rather than a mistake rate.
	for _, want := range []string{
		"MARKER COMPLIANCE, not a mistake rate",
		"forward-only",
		"marking artifact",
		"criterion 4: PASS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("evaluation rendering no longer states %q", want)
		}
	}
}

func TestDefaultCommandIsOverridable(t *testing.T) {
	t.Setenv(EnvCommand, "claude -p --model some-other-model")
	got := DefaultCommand()
	want := []string{"claude", "-p", "--model", "some-other-model"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("DefaultCommand()=%v, want %v — the model must be pinnable by configuration, not by editing Go", got, want)
	}
}
