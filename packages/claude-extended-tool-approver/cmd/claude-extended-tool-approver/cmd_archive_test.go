package main

// TRUE UNIT TESTS for the archive subcommand's PURE logic — no sqlite store,
// no compiled binary, so (per cmd_evaluate_test.go's own file-header
// rationale) this file is deliberately untagged and runs under a plain
// `go test ./...`. DB-backed archive coverage (real rows, real deletes,
// real VACUUM) lives in cmd_archive_integration_test.go behind the
// `integration` build tag instead.

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// TestResolveArchiveThreshold pins the --before/--days precedence and the
// refuse-rather-than-guess behavior when both or neither are given.
func TestResolveArchiveThreshold(t *testing.T) {
	got, err := resolveArchiveThreshold(archiveOptions{Before: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("--before alone: unexpected error: %v", err)
	}
	if got != "2026-01-01T00:00:00Z" {
		t.Errorf("--before alone: got %q, want the literal value back", got)
	}

	got, err = resolveArchiveThreshold(archiveOptions{Days: 30})
	if err != nil {
		t.Fatalf("--days alone: unexpected error: %v", err)
	}
	if got == "" {
		t.Error("--days alone: got empty threshold, want a computed ISO8601 date")
	}

	if _, err := resolveArchiveThreshold(archiveOptions{}); err == nil {
		t.Error("neither --before nor --days: want an error, got nil")
	}

	if _, err := resolveArchiveThreshold(archiveOptions{Before: "2026-01-01T00:00:00Z", Days: 30}); err == nil {
		t.Error("both --before and --days: want an error (mutually exclusive), got nil")
	}
}

// TestArchiveBucketKey_AnnotatedMissOutranksOutcome pins the priority
// classification: a row with a ground-truth annotation that DISAGREES with
// the hook is always "annotated-miss", regardless of outcome, since that is
// the single most valuable row category to keep.
func TestArchiveBucketKey_AnnotatedMissOutranksOutcome(t *testing.T) {
	allow := "allow"
	deny := "deny"
	row := asklog.ArchiveRow{
		ToolName:            "Bash",
		Outcome:             "approved",
		HookDecision:        &allow,
		CorrectHookDecision: &deny,
	}
	got := archiveBucketKey(row)
	if got != "Bash|annotated-miss" {
		t.Errorf("archiveBucketKey = %q, want %q", got, "Bash|annotated-miss")
	}
}

// TestArchiveBucketKey_AnnotatedMatch pins the sibling case: an annotation
// that AGREES with the hook is "annotated-match", not folded into the
// unannotated bucket.
func TestArchiveBucketKey_AnnotatedMatch(t *testing.T) {
	allow := "allow"
	row := asklog.ArchiveRow{
		ToolName:            "Bash",
		Outcome:             "approved",
		HookDecision:        &allow,
		CorrectHookDecision: &allow,
	}
	got := archiveBucketKey(row)
	if got != "Bash|annotated-match" {
		t.Errorf("archiveBucketKey = %q, want %q", got, "Bash|annotated-match")
	}
}

// TestArchiveBucketKey_UnannotatedBucketsByOutcome pins the fallback: a
// never-annotated row buckets by (tool_name, outcome), so denied/rejected
// edge cases stay distinguishable from routine approvals.
func TestArchiveBucketKey_UnannotatedBucketsByOutcome(t *testing.T) {
	for _, tc := range []struct {
		toolName, outcome, want string
	}{
		{"Bash", "approved", "Bash|unannotated:approved"},
		{"Bash", "denied", "Bash|unannotated:denied"},
		{"Read", "approved", "Read|unannotated:approved"},
	} {
		row := asklog.ArchiveRow{ToolName: tc.toolName, Outcome: tc.outcome}
		if got := archiveBucketKey(row); got != tc.want {
			t.Errorf("archiveBucketKey(%s,%s) = %q, want %q", tc.toolName, tc.outcome, got, tc.want)
		}
	}
}

// makeRows builds n synthetic ArchiveRows with distinct, ascending IDs — a
// stand-in for a QueryRowsBefore result, since these tests are pure and
// never touch a database.
func makeRows(n int) []asklog.ArchiveRow {
	rows := make([]asklog.ArchiveRow, n)
	for i := range rows {
		rows[i] = asklog.ArchiveRow{ID: i + 1, ToolName: "Bash", Outcome: "approved"}
	}
	return rows
}

func idsOf(rows []asklog.ArchiveRow) []int {
	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// TestPickSample_ReturnsEverythingWhenUnderLimit: no thinning needed when
// there are already at most n rows.
func TestPickSample_ReturnsEverythingWhenUnderLimit(t *testing.T) {
	rows := makeRows(3)
	got := pickSample(rows, 5)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (all rows, under the limit)", len(got))
	}
}

// TestPickSample_SpreadsAcrossTheWholeRange: sampling 5 from 100 must
// include the first and last row, not just an early cluster — that is what
// "representative of the whole archived window" requires.
func TestPickSample_SpreadsAcrossTheWholeRange(t *testing.T) {
	rows := makeRows(100)
	got := pickSample(rows, 5)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	ids := idsOf(got)
	if ids[0] != 1 {
		t.Errorf("first sampled id = %d, want 1 (the earliest candidate)", ids[0])
	}
	if ids[len(ids)-1] != 100 {
		t.Errorf("last sampled id = %d, want 100 (the latest candidate)", ids[len(ids)-1])
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("sampled ids not strictly increasing: %v", ids)
			break
		}
	}
}

// TestPickSample_OneRequestedRow: n=1 must not divide by zero and must
// return exactly one row.
func TestPickSample_OneRequestedRow(t *testing.T) {
	rows := makeRows(10)
	got := pickSample(rows, 1)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != 1 {
		t.Errorf("sampled id = %d, want 1", got[0].ID)
	}
}

// TestPickSample_ZeroOrNegative_ReturnsAll: a non-positive n means "no cap".
func TestPickSample_ZeroOrNegative_ReturnsAll(t *testing.T) {
	rows := makeRows(4)
	for _, n := range []int{0, -1} {
		got := pickSample(rows, n)
		if len(got) != 4 {
			t.Errorf("pickSample(rows, %d): len = %d, want 4 (all rows)", n, len(got))
		}
	}
}

// TestSampleFixtures_CapsPerBucketAndKeepsBucketsSeparate: two tool_name
// buckets, each with more candidates than the per-bucket cap, must each be
// thinned independently — a busy bucket must not crowd out a quiet one.
func TestSampleFixtures_CapsPerBucketAndKeepsBucketsSeparate(t *testing.T) {
	var candidates []asklog.ArchiveRow
	for i := 1; i <= 20; i++ {
		candidates = append(candidates, asklog.ArchiveRow{ID: i, ToolName: "Bash", Outcome: "approved"})
	}
	for i := 21; i <= 23; i++ {
		candidates = append(candidates, asklog.ArchiveRow{ID: i, ToolName: "Read", Outcome: "approved"})
	}

	sampled := sampleFixtures(candidates, 3)

	var bashCount, readCount int
	for _, r := range sampled {
		switch r.ToolName {
		case "Bash":
			bashCount++
		case "Read":
			readCount++
		}
	}
	if bashCount != 3 {
		t.Errorf("Bash sampled = %d, want 3 (capped)", bashCount)
	}
	if readCount != 3 {
		t.Errorf("Read sampled = %d, want 3 (all of them, under the cap)", readCount)
	}
}

// TestSampleFixtures_AnnotatedMissAlwaysSurvivesASmallCap: the whole point
// of bucketing by classification is that a single confirmed-miss row, even
// amid a flood of routine approvals for the same tool, is never crowded out
// by the cap on the UNRELATED unannotated bucket.
func TestSampleFixtures_AnnotatedMissAlwaysSurvivesASmallCap(t *testing.T) {
	allow := "allow"
	deny := "deny"
	var candidates []asklog.ArchiveRow
	for i := 1; i <= 50; i++ {
		candidates = append(candidates, asklog.ArchiveRow{ID: i, ToolName: "Bash", Outcome: "approved"})
	}
	candidates = append(candidates, asklog.ArchiveRow{
		ID: 999, ToolName: "Bash", Outcome: "approved",
		HookDecision: &allow, CorrectHookDecision: &deny,
	})

	sampled := sampleFixtures(candidates, 2)

	found := false
	for _, r := range sampled {
		if r.ID == 999 {
			found = true
		}
	}
	if !found {
		t.Error("the single annotated-miss row (id 999) was not in the sample; it must survive independently of the unannotated bucket's cap")
	}
}

// TestSampleFixtures_EmptyInput: no candidates means no sample, no panic.
func TestSampleFixtures_EmptyInput(t *testing.T) {
	got := sampleFixtures(nil, 5)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestArchiveBucketKey_MissingHookDecisionTreatedAsEmpty: a row with a
// ground-truth annotation but a NULL hook_decision (pre-migration data)
// must not panic dereferencing a nil pointer, and must classify as a miss
// whenever the annotation names any concrete decision (never equal to "").
func TestArchiveBucketKey_MissingHookDecisionTreatedAsEmpty(t *testing.T) {
	deny := "deny"
	row := asklog.ArchiveRow{
		ToolName:            "Bash",
		Outcome:             "approved",
		HookDecision:        nil,
		CorrectHookDecision: &deny,
	}
	got := archiveBucketKey(row)
	if got != "Bash|annotated-miss" {
		t.Errorf("archiveBucketKey = %q, want %q", got, "Bash|annotated-miss")
	}
}
