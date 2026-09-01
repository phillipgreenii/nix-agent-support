package main

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/cache"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
	"github.com/phillipgreenii/pg-ccaudit/internal/ledger"
)

// TestClassifyStatusReportsAllPendingBeforeAnythingIsCached covers the plain
// CLI surface (bead pg2-ohvpk requirement 2) with no prior cache or ledger
// data: every candidate in the window must show as pending, and the $
// projection must say so is unknown rather than fabricating a number.
func TestClassifyStatusReportsAllPendingBeforeAnythingIsCached(t *testing.T) {
	mistakeIndex(t)
	out, _, err := captureRun(t, "classify", "status", "--classifier", "baseline")
	if err != nil {
		t.Fatalf("classify status: %v", err)
	}
	for _, want := range []string{
		"classify status window=all classifier=baseline",
		"17 total, 0 cached, 17 pending",
		"cost unknown — no prior run recorded in the cost ledger yet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("classify status is missing %q:\n%s", want, out)
		}
	}
}

func TestClassifyStatusJSON(t *testing.T) {
	mistakeIndex(t)
	out, _, err := captureRun(t, "classify", "status", "--classifier", "baseline", "--format", "json")
	if err != nil {
		t.Fatalf("classify status --format json: %v", err)
	}
	for _, want := range []string{`"cached": 0`, `"pending": 17`, `"cost_history_available": false`} {
		if !strings.Contains(out, want) {
			t.Errorf("classify status --format json is missing %q:\n%s", want, out)
		}
	}
}

// TestClassifyStatusSeesEntriesCachedByAnEarlierRun is the "classify status"
// half of pg2-ohvpk's testable claim, exercised directly through the CLI
// dispatcher rather than through runClassifierStreaming: a candidate cached
// under a given classifier and prompt version must be reported cached, not
// pending, and the cost projection must be seeded from the ledger's
// measured $/call for that same classifier.
func TestClassifyStatusSeesEntriesCachedByAnEarlierRun(t *testing.T) {
	mistakeIndex(t)
	t.Setenv(classify.EnvCommand, "fake")

	// newClassifier("cli") with PG_CCAUDIT_CLASSIFY_CMD=fake names itself
	// "cli:fake" — seed the cache and ledger under exactly that name, as if
	// an earlier `report`/`classify` run had already classified 3
	// candidates and paid for 2 calls doing it.
	cachePath, err := cache.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	set := extractAll(t)
	if len(set) < 3 {
		t.Fatalf("fixture has only %d candidates, need at least 3", len(set))
	}
	var entries []cache.Entry
	for _, id := range set[:3] {
		entries = append(entries, cache.Entry{ID: id, Classifier: "cli:fake", PromptVersion: classify.PromptVersion, Class: "not-a-mistake"})
	}
	if err := cache.Append(cachePath, entries); err != nil {
		t.Fatal(err)
	}

	ledgerPath, err := ledger.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(ledgerPath, ledger.Entry{
		RunID: "seed-run", Command: "report", Classifier: "cli:fake",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Calls: 2, USD: 0.10, Done: true,
	}); err != nil {
		t.Fatal(err)
	}

	out, _, err := captureRun(t, "classify", "status", "--classifier", "cli")
	if err != nil {
		t.Fatalf("classify status: %v", err)
	}
	for _, want := range []string{
		"17 total, 3 cached, 14 pending",
		// batch defaults to classify.DefaultBatch (10): ceil(14/10) = 2 calls.
		"projected: 2 call(s)",
		"seeded from 2 historical call(s) averaging $0.0500/call",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("classify status is missing %q:\n%s", want, out)
		}
	}
}

func TestClassifyUnknownSubcommandIsAUsageError(t *testing.T) {
	mistakeIndex(t)
	_, errOut, err := captureRun(t, "classify", "bogus")
	if err == nil {
		t.Fatal("an unknown classify subcommand must be rejected")
	}
	if !strings.Contains(errOut, `unknown subcommand "bogus"`) {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestCostReportsNoLedgerYet(t *testing.T) {
	mistakeIndex(t)
	out, _, err := captureRun(t, "cost")
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	if !strings.Contains(out, "does not exist yet") {
		t.Errorf("cost output = %q", out)
	}
}

// TestCostReportsSeededRunsAndRespectsWindow covers requirement 3's CLI
// surface: cumulative spend by run, and the --since/--until window filter.
func TestCostReportsSeededRunsAndRespectsWindow(t *testing.T) {
	mistakeIndex(t)
	ledgerPath, err := ledger.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	early := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if err := ledger.Append(ledgerPath, ledger.Entry{
		RunID: "run-early", Command: "classify", Classifier: "cli:fake",
		StartedAt: early, UpdatedAt: early, Calls: 3, USD: 0.15, Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(ledgerPath, ledger.Entry{
		RunID: "run-late", Command: "report", Classifier: "cli:fake",
		StartedAt: late, UpdatedAt: late, Calls: 1, USD: 0.02, Done: false,
	}); err != nil {
		t.Fatal(err)
	}

	out, _, err := captureRun(t, "cost")
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	for _, want := range []string{"run-early", "run-late", "2 run(s), 4 call(s) total, $0.1700 total"} {
		if !strings.Contains(out, want) {
			t.Errorf("cost is missing %q:\n%s", want, out)
		}
	}

	out, _, err = captureRun(t, "cost", "--since", "2026-08-10")
	if err != nil {
		t.Fatalf("cost --since: %v", err)
	}
	if strings.Contains(out, "run-early") {
		t.Errorf("--since must exclude the earlier run:\n%s", out)
	}
	if !strings.Contains(out, "run-late") {
		t.Errorf("--since must keep the later run:\n%s", out)
	}
}

// extractAll returns every Tier 1 candidate id in the fixture's full window,
// using the same extract() helper the commands themselves use.
func extractAll(t *testing.T) []string {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cf := addCensusFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	db, _, err := openIndex(*cf.db)
	if err != nil {
		t.Fatalf("openIndex: %v", err)
	}
	defer func() { _ = db.Close() }()
	set, err := extract(context.Background(), db, cf)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	ids := make([]string, 0, len(set.Candidates))
	for _, c := range set.Candidates {
		ids = append(ids, classify.CandidateID(c))
	}
	return ids
}
