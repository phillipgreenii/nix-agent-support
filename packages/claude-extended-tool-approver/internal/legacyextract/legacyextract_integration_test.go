//go:build integration

// The converter tests below parse the repo's own legacy Go test files with
// go/parser and replay thousands of rows through the engine: a migration tool
// run, not a unit test (V-4). They run in the `integration` tier, i.e. the
// claude-extended-tool-approver-integration-tests check.

package legacyextract

import (
	"os"
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// repoRoot is the claude-extended-tool-approver module root, relative to
// this package's own directory (internal/legacyextract).
const repoRoot = "../.."

// outputCorpusPath / reportPath are where TestLegacyExtract (this packet's
// own "run the converter" step -- see the docket Validation section's
// `go test ./internal/... -run TestLegacyExtract` example) writes its two
// checked-in artifacts: the legacy-tagged corpus rows, and the disposition
// report the docket design's own acceptance criteria require. A SEPARATE
// file from goldencorpus's own hand-seeded testdata/corpus.json (Phase
// 0.2's own "introduce a second array file the loader concatenates"
// freedom), so this packet never touches that packet's own file.
const (
	outputCorpusPath = "../goldencorpus/testdata/corpus_legacy.json"
	reportPath       = "../goldencorpus/testdata/legacy_disposition_report.md"
)

// legacyExtractWriteEnv gates writing the two checked-in output artifacts.
// A bare `go test ./...` (this repo's pre-commit `run-unit-tests` hook runs
// exactly that) must NOT mutate tracked files as a side effect of running
// the test suite -- doing so fights the SAME commit's `treefmt` hook, which
// reformats generated JSON/Markdown (prettier-style) differently from this
// package's own `json.MarshalIndent`/plain-text output, so every hook run
// would perpetually "fix" what the previous run just wrote (observed
// directly while validating this packet: `prek` oscillated between the two
// formats across repeated invocations). Regenerating the artifacts is
// therefore an explicit, opt-in action: `LEGACYEXTRACT_WRITE=1 go test
// ./internal/legacyextract/... -run TestLegacyExtract`, or `go run
// ./cmd/legacyextract -write` -- either way, run treefmt/prek over the
// result afterward before committing, exactly as any other generated file
// in this repo.
const legacyExtractWriteEnv = "LEGACYEXTRACT_WRITE"

// TestLegacyExtract is docket tc-o14i5.1.3's own converter entry point: run
// the legacy RuleChain/internal-rules table-test extraction, compare every
// extracted row against the new effect engine, and report the packet's own
// re-counted exit-criterion ratio (extracted / actual-recounted-
// denominator -- NOT the design's ~2,969 heuristic figure). Writes the two
// checked-in artifacts only when legacyExtractWriteEnv is set (see its own
// doc comment for why a bare `go test` must not).
func TestLegacyExtract(t *testing.T) {
	res, err := Run(repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.Denominator == 0 {
		t.Fatal("denominator is 0 -- extraction found no recognised legacy tables at all (regression: internal/engine or internal/rules paths are probably wrong)")
	}
	// Regression floor, not the 95% exit bar itself: catches a future edit
	// that silently guts the extractor's recognised-idiom coverage, without
	// re-asserting the exact count (which will drift as legacy tests
	// themselves change before Phase 0.5 removes them).
	const minExtractedFloor = 2000
	if res.Stats.Extracted < minExtractedFloor {
		t.Errorf("extracted %d rows, want at least %d (regression floor; see t.Log for the full ratio)", res.Stats.Extracted, minExtractedFloor)
	}
	for _, r := range res.Rows {
		if !r.ExpectedVerdict.Valid() {
			t.Errorf("case %q: expected_verdict %q is not one of {approve, reject, not-approve}", r.Case, r.ExpectedVerdict)
		}
		if r.ToolInput.ToolName == "" {
			t.Errorf("case %q: empty tool_name", r.Case)
		}
	}
	seen := map[string]bool{}
	for _, r := range res.Rows {
		if seen[r.Case] {
			t.Errorf("duplicate case name %q", r.Case)
		}
		seen[r.Case] = true
	}

	cmp, err := CompareAgainstNewEngine(res.Rows)
	if err != nil {
		t.Fatalf("CompareAgainstNewEngine: %v", err)
	}

	if os.Getenv(legacyExtractWriteEnv) != "" {
		if err := WriteCorpus(outputCorpusPath, res.Rows); err != nil {
			t.Fatalf("writing %s: %v", outputCorpusPath, err)
		}
		if err := WriteDispositionReport(reportPath, res, cmp); err != nil {
			t.Fatalf("writing %s: %v", reportPath, err)
		}
	} else {
		t.Logf("legacyextract: %s not set -- skipping write of %s / %s (see legacyExtractWriteEnv doc comment)", legacyExtractWriteEnv, outputCorpusPath, reportPath)
	}

	t.Logf("legacyextract: files=%d recognised-tables=%d denominator=%d extracted=%d ratio=%.4f (exit bar: >= 0.95, else remainder listed in %s)",
		res.Stats.FilesScanned, res.Stats.RecognisedTables, res.Stats.Denominator, res.Stats.Extracted, res.Stats.Ratio(), reportPath)
	t.Logf("legacyextract: fake/stub files=%d, tempdir files=%d (manual-disposition-per-family carve-out, docket design Phase 0 item 3)", len(res.FakeStubFiles), len(res.TempDirFiles))
	t.Logf("legacyextract: comparable=%d not-comparable=%d agreed=%d disagreements=%d", cmp.Comparable, cmp.NotComparable, cmp.Agreed, len(cmp.Disagreements))
}

// TestLegacyPnwfRowsGradeAgainstMarkerDirectory is the regression guard for
// pg2-82oo9: the legacy pnwf oracle rows that approve only from a trusted exec
// location MUST be extracted with workspace_markers and MUST agree with the new
// engine; and a marker directory must not flip any legacy not-approve/reject
// row to approve.
func TestLegacyPnwfRowsGradeAgainstMarkerDirectory(t *testing.T) {
	res, err := Run(repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cmp, err := CompareAgainstNewEngine(res.Rows)
	if err != nil {
		t.Fatalf("CompareAgainstNewEngine: %v", err)
	}
	disagreed := map[string]Disagreement{}
	for _, d := range cmp.Disagreements {
		disagreed[d.Case] = d
	}

	const prefix = "legacy_internal_rules_pnwf_pnwf_test_"
	want := []string{
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row0", // pnwf sync-fetch
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row1", // pnwf sync-fetch --set
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row2", // pnwf update-relock
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row3", // pnwf update-relock --set
		prefix + "TestPnwf_Cleanup_NoForceFlag_Approve_row0",   // pnwf cleanup my-branch
	}
	byCase := map[string]goldencorpus.Row{}
	for _, r := range res.Rows {
		byCase[r.Case] = r
	}
	for _, c := range want {
		r, ok := byCase[c]
		if !ok {
			t.Errorf("case %q not extracted", c)
			continue
		}
		if r.ExpectedVerdict != goldencorpus.Approve {
			t.Errorf("case %q: legacy oracle verdict = %s, want approve", c, r.ExpectedVerdict)
		}
		if !reflect.DeepEqual(r.CWDPathState.WorkspaceMarkers, []string{"pn-workspace.toml"}) {
			t.Errorf("case %q: workspace_markers = %v, want [pn-workspace.toml]", c, r.CWDPathState.WorkspaceMarkers)
		}
		if d, bad := disagreed[c]; bad {
			t.Errorf("case %q: new engine disagrees with the legacy oracle (old=%s new=%s: %s)", c, d.OldVerdict, d.NewVerdict, d.NewReason)
		}
	}

	// Widening guard: the marker directory exists to restore the approvals the
	// legacy oracle expects, so it MUST NOT approve a marked row that the
	// same row does not approve without a marker directory (the synthetic cwd),
	// unless the legacy oracle itself approved it. Replay every marked row both ways.
	var marked, unmarked []goldencorpus.Row
	for _, r := range res.Rows {
		if len(r.CWDPathState.WorkspaceMarkers) == 0 {
			continue
		}
		marked = append(marked, r)
		r.CWDPathState.WorkspaceMarkers = nil
		unmarked = append(unmarked, r)
	}
	if len(marked) == 0 {
		t.Fatal("no row was extracted with workspace_markers")
	}
	approvesIn := func(rows []goldencorpus.Row) map[string]bool {
		c, err := CompareAgainstNewEngine(rows)
		if err != nil {
			t.Fatalf("CompareAgainstNewEngine: %v", err)
		}
		out := map[string]bool{}
		dis := map[string]Disagreement{}
		for _, d := range c.Disagreements {
			dis[d.Case] = d
		}
		for _, r := range rows {
			if d, bad := dis[r.Case]; bad {
				out[r.Case] = d.NewVerdict == goldencorpus.Approve
			} else {
				out[r.Case] = r.ExpectedVerdict == goldencorpus.Approve
			}
		}
		return out
	}
	withMarker, withoutMarker := approvesIn(marked), approvesIn(unmarked)
	for _, r := range marked {
		if r.ExpectedVerdict != goldencorpus.Approve && withMarker[r.Case] && !withoutMarker[r.Case] {
			t.Errorf("case %q: legacy %s, approves only because of the marker directory", r.Case, r.ExpectedVerdict)
		}
	}
}
