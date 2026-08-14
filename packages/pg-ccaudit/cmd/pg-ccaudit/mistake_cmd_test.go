package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// mistakeIndex builds an index over the committed mistake fixture in t.TempDir() and
// points the process env at it. Nothing here reaches the real index, the real corpus,
// or a model.
func mistakeIndex(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	src := filepath.Join("..", "..", "internal", "ingest", "testdata", "mistakes")
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
	t.Setenv("PG_CCAUDIT_DB", dbPath)
	t.Setenv("PG_CCAUDIT_ROOT", root)
	t.Setenv("PG_CCAUDIT_GOLD", filepath.Join(base, "goldset.jsonl"))
	return base
}

// TestParseInterspersedFixesTheDocumentedInvocation is the regression for a defect in
// the shipped CLI: Go's flag package stops at the first non-flag token, so
// `query <name> --since X` handed `--since` to the query as a positional argument and
// the command failed — while that is exactly the form the review skill documents.
func TestParseInterspersedFixesTheDocumentedInvocation(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	since := fs.String("since", "", "")
	until := fs.String("until", "", "")

	pos, err := parseInterspersed(fs, []string{"top-signatures", "3", "--since", "2026-07-22", "--until", "2026-07-30"})
	if err != nil {
		t.Fatalf("parseInterspersed: %v", err)
	}
	if len(pos) != 2 || pos[0] != "top-signatures" || pos[1] != "3" {
		t.Errorf("positionals=%v, want [top-signatures 3]", pos)
	}
	if *since != "2026-07-22" || *until != "2026-07-30" {
		// A window silently not applied means the census covered a different period than
		// it claimed to, which is the class of unverifiable number this index exists to
		// eliminate.
		t.Errorf("since=%q until=%q — flags AFTER a positional must still parse", *since, *until)
	}

	// Flags before, between and after must all work.
	fs2 := flag.NewFlagSet("t2", flag.ContinueOnError)
	fs2.SetOutput(io.Discard)
	s2 := fs2.String("since", "", "")
	pos2, err := parseInterspersed(fs2, []string{"--since", "x", "name", "arg"})
	if err != nil {
		t.Fatalf("parseInterspersed: %v", err)
	}
	if *s2 != "x" || len(pos2) != 2 || pos2[0] != "name" {
		t.Errorf("since=%q positionals=%v", *s2, pos2)
	}
}

func TestQueryAcceptsTheSkillsDocumentedForm(t *testing.T) {
	mistakeIndex(t)
	// This is verbatim the shape SKILL.md tells an agent to run.
	out, _, err := captureRun(t, "query", "human-turns", "--since", "2026-08-01",
		"--until", "2026-08-02", "--no-staleness", "--format", "tsv")
	if err != nil {
		t.Fatalf("query with trailing flags failed: %v", err)
	}
	if !strings.Contains(out, "window=[2026-08-01,2026-08-02)") {
		t.Errorf("the window was not applied; output was:\n%s", out)
	}
}

func TestCandidatesIsReadOnlyAndReportsProvenance(t *testing.T) {
	base := mistakeIndex(t)
	dbPath := filepath.Join(base, "transcripts.db")
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	out, errOut, err := captureRun(t, "candidates")
	if err != nil {
		t.Fatalf("candidates: %v\n%s", err, errOut)
	}
	for _, want := range []string{
		"typed-turn-candidates    v1 rows=3",
		"interruptions            v1 rows=1",
		"denied-tool-calls        v1 rows=2",
		"undo-signatures          v1 rows=4",
		"file-churn               v1 rows=1",
		"escaping-retries         v1 rows=1",
		"ack-markers              v1 rows=4",
		"# total candidates: 17",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("candidates output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "SUPPLEMENTARY") {
		t.Error("an acknowledgment candidate must be marked SUPPLEMENTARY in the output")
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// The index is shared with running sessions, so a census must never write to it.
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("candidates modified the database; the census path must be read-only")
	}
}

func TestClassifyReportsCostEvenWithTheZeroCostBaseline(t *testing.T) {
	mistakeIndex(t)
	_, errOut, err := captureRun(t, "classify", "--classifier", "baseline")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	// The cost line is unconditional: a pass whose cost nobody sees is a pass that
	// quietly grows with the corpus.
	for _, want := range []string{"classify complete:", "candidates_in=17", "calls=0", "usd=0.0000", "truncated=false"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("cost line is missing %q:\n%s", want, errOut)
		}
	}
}

func TestClassifyReportsTruncation(t *testing.T) {
	mistakeIndex(t)
	_, errOut, err := captureRun(t, "classify", "--classifier", "baseline", "--max", "3")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	// Every rate computed downstream is over the truncated set. A silently partial
	// pass presented as a census is exactly the miscount this index exists to prevent.
	if !strings.Contains(errOut, "truncated=true") || !strings.Contains(errOut, "candidates_in=3") {
		t.Errorf("a bounded run must report itself truncated:\n%s", errOut)
	}
}

func TestReportRoutesEveryFindingAndStatesTheUndercount(t *testing.T) {
	mistakeIndex(t)
	out, _, err := captureRun(t, "report", "--classifier", "baseline", "--no-evaluation")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, want := range []string{
		"# pg-ccaudit mistake census",
		"## Provenance",
		"score = occurrences x (1 + cost_ms/1000) x preventability(route)",
		"STRUCTURALLY INVISIBLE",
		"## Routing totals — every finding carries exactly one route",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	// Criterion 8: the report must split at least one finding on is_sidechain. The
	// fixture's git-undo class is one main-loop occurrence and one subagent.
	if !strings.Contains(out, "subagent=1") {
		t.Errorf("no finding was split by is_sidechain:\n%s", out)
	}
	// Every rendered finding line carries a route in brackets; none may be blank.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "1. ") || strings.HasPrefix(line, "2. ") {
			if !strings.Contains(line, "[") || strings.Contains(line, "[]") {
				t.Errorf("finding line has no route: %q", line)
			}
		}
	}
}

func TestReportWarnsWhenTheGoldSetIsAbsent(t *testing.T) {
	mistakeIndex(t)
	_, errOut, err := captureRun(t, "report", "--classifier", "baseline", "--no-evaluation")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	// A file-channel count of 0 with no gold set is a MISSING MEASUREMENT, not a
	// measured zero, and the difference decides whether the report's correction count
	// can be quoted at all.
	if !strings.Contains(errOut, "MISSING MEASUREMENT") {
		t.Errorf("an absent gold set must be reported as a missing measurement:\n%s", errOut)
	}
}

func TestGoldSeedSampleAndStatus(t *testing.T) {
	base := mistakeIndex(t)
	feedbackDir := filepath.Join(base, "projects", "-Users-x-repo", "memory")
	if err := os.MkdirAll(feedbackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feedbackDir, "feedback_example.md"),
		[]byte("# Do not do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(base, "FEEDBACK.md")
	if err := os.WriteFile(extra, []byte("# raw critique\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := captureRun(t, "gold", "seed", "--memory-root",
		filepath.Join(base, "projects"), "--feedback", extra)
	if err != nil {
		t.Fatalf("gold seed: %v", err)
	}
	if !strings.Contains(out, "seeded 2 file-channel correction(s)") {
		t.Errorf("gold seed found the wrong number of file-channel sources:\n%s", out)
	}

	if _, _, err := captureRun(t, "gold", "sample", "--sample", "6"); err != nil {
		t.Fatalf("gold sample: %v", err)
	}
	out, _, err = captureRun(t, "gold", "status")
	if err != nil {
		t.Fatalf("gold status: %v", err)
	}
	for _, want := range []string{
		"unlabelled candidates: 6",
		"file-channel entries:  2",
		"criterion 4 needs at least 50 labelled candidates",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("gold status is missing %q:\n%s", want, out)
		}
	}

	// Re-seeding must not disturb the sampled rows.
	if _, _, err := captureRun(t, "gold", "seed", "--memory-root",
		filepath.Join(base, "projects"), "--feedback", extra); err != nil {
		t.Fatalf("gold re-seed: %v", err)
	}
	out, _, err = captureRun(t, "gold", "status")
	if err != nil {
		t.Fatalf("gold status: %v", err)
	}
	if !strings.Contains(out, "unlabelled candidates: 6") {
		t.Errorf("re-seeding disturbed the sample:\n%s", out)
	}
}

func TestGoldSampleIsStratifiedAcrossSignals(t *testing.T) {
	mistakeIndex(t)
	if _, _, err := captureRun(t, "gold", "sample", "--sample", "8"); err != nil {
		t.Fatalf("gold sample: %v", err)
	}
	b, err := os.ReadFile(os.Getenv("PG_CCAUDIT_GOLD"))
	if err != nil {
		t.Fatal(err)
	}
	signals := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		for _, sig := range []string{"typed-turn", "interruption", "denied-tool-call", "undo", "churn", "escaping-retry", "ack"} {
			if strings.Contains(line, `"signal":"`+sig+`"`) {
				signals[sig]++
			}
		}
	}
	// Taking the FIRST N would be almost all typed turns — the highest-volume signal —
	// leaving every rarer class with a support of zero, whose precision then reports as
	// 0.000 and reads as "the classifier is bad at it" when it means "nothing was
	// measured".
	if len(signals) < 5 {
		t.Errorf("a sample of 8 covered only %d signals: %v", len(signals), signals)
	}
}

func TestEvaluateFailsLoudlyWhenTheBaselineIsNotBeaten(t *testing.T) {
	base := mistakeIndex(t)
	// A gold set that labels the fixture's two typed turns as corrections is exactly
	// what the baseline rule predicts, so the baseline cannot beat itself.
	gold := filepath.Join(base, "goldset.jsonl")
	body := ""
	for _, id := range []string{
		"typed-turn:" + filepath.Join(base, "projects", "projM", "sess-m.jsonl") + "#6",
		"typed-turn:" + filepath.Join(base, "projects", "projM", "sess-m.jsonl") + "#20",
	} {
		body += `{"id":"` + id + `","source":"hand-labelled","class":"user-correction","labeller":"test"}` + "\n"
	}
	if err := os.WriteFile(gold, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := captureRun(t, "evaluate", "--classifier", "baseline")
	if err == nil {
		// Criterion 4 says a classifier that cannot beat the baseline MUST be reworked,
		// not shipped, so the exit status has to carry that — a reader should not have to
		// spot the word FAIL in a wall of numbers.
		t.Fatal("evaluate must exit non-zero when criterion 4 is not met")
	}
	if !strings.Contains(err.Error(), "criterion 4") {
		t.Errorf("the error must name criterion 4, got %v", err)
	}
}

func TestUnknownClassifierIsRejected(t *testing.T) {
	mistakeIndex(t)
	_, _, err := captureRun(t, "classify", "--classifier", "magic")
	if err == nil {
		t.Fatal("an unknown classifier must be rejected")
	}
}
