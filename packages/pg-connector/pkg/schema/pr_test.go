package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPR_JSONRoundTrip(t *testing.T) {
	in := PR{
		ID:     "pr-1",
		Repo:   "owner/repo",
		Number: 42,
		Title:  "Add feature",
		State:  "open",
		Branch: "feature",
		Base:   "main",
		Author: "octocat",
		URL:    "https://example.invalid/owner/repo/pull/42",
		Draft:  false,
		Merged: false,
		Comments: []PRComment{
			{ID: "c1", Author: "octocat", Body: "looks good", Resolved: false},
		},
		Reviews: []PRReview{
			{
				ID:     "r1",
				Author: "reviewer",
				State:  "CHANGES_REQUESTED",
				Comments: []PRComment{
					{ID: "c2", Author: "reviewer", Body: "fix this", ThreadID: "t1"},
				},
			},
		},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID {
		t.Fatalf("round-trip mismatch: got %+v", out)
	}
	if len(out.Comments) != 1 || out.Comments[0].ID != "c1" {
		t.Fatalf("comments round-trip mismatch: got %+v", out.Comments)
	}
	if len(out.Reviews) != 1 || len(out.Reviews[0].Comments) != 1 || out.Reviews[0].Comments[0].ID != "c2" {
		t.Fatalf("review-thread comments round-trip mismatch: got %+v", out.Reviews)
	}
}

func TestPR_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	// AsOf/Stale (bead pg2-681xo) are not omitempty — Stale in particular
	// must always be present, since false is itself informative (matching
	// PR's other plain-bool facts, Draft/Merged), and a consumer must be
	// able to distinguish "explicitly not stale" from "field absent."
	raw, err := json.Marshal(PR{ID: "pr-1", AsOf: "2026-09-06T00:00:00Z", Stale: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["as_of"]; !ok {
		t.Fatalf("as_of missing from %s", raw)
	}
	staleVal, ok := out["stale"]
	if !ok {
		t.Fatalf("stale missing from %s", raw)
	}
	if staleVal != false {
		t.Fatalf("stale = %v, want false", staleVal)
	}
}

func TestPR_CommentIDAndCommentIDAreStrings(t *testing.T) {
	// PR.ID and PRComment.ID must be strings, carried over as-is from
	// pg-pr's api.Comment.ID string field — a compile-time assertion that
	// these fields are string-typed, not numeric.
	var _ string = PR{}.ID        //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
	var _ string = PRComment{}.ID //nolint:staticcheck // QF1011: same as above.
}

// TestPRSchemaVersion_IsCurrent pins PRSchemaVersion at its current value
// (the review-context change to show bumped 6 -> 7) so an accidental future edit that
// forgets to bump it alongside a new field-shape change is caught here
// first.
func TestPRSchemaVersion_IsCurrent(t *testing.T) {
	if PRSchemaVersion != 7 {
		t.Fatalf("PRSchemaVersion = %d, want 7", PRSchemaVersion)
	}
}

// TestPR_V5FieldSet_JSONRoundTrip asserts the v5 summary field set (bead
// pg2-2j5ac.52.6.1's additive fields: NodeID, UpdatedAt, ReviewDecision,
// CommentCount, ReviewCount) round-trips through JSON under its exact wire
// keys.
func TestPR_V5FieldSet_JSONRoundTrip(t *testing.T) {
	in := PR{
		ID:             "pr-1",
		NodeID:         "PR_kwDOSynthetic1",
		UpdatedAt:      "2026-09-14T10:00:00Z",
		ReviewDecision: "CHANGES_REQUESTED",
		CommentCount:   4,
		ReviewCount:    2,
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{
		"node_id", "updated_at", "review_decision", "comment_count", "review_count",
	} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("wire JSON missing %q key: %s", key, raw)
		}
	}
}

// TestPR_V5FieldSet_OmittedWhenEmpty asserts every v5 field is omitempty: a
// backend that cannot provide one leaves it empty/zero and the key is absent
// from the wire, never a fabricated placeholder.
func TestPR_V5FieldSet_OmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(PR{ID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{
		"node_id", "updated_at", "review_decision", "comment_count", "review_count",
	} {
		if _, ok := asMap[key]; ok {
			t.Errorf("wire JSON carries %q for an empty value: %s", key, raw)
		}
	}
}

// TestPR_V4FieldSet_JSONRoundTrip asserts the full v4 field set (bead
// pg2-2j5ac.28.2's additive fields: HeadSHA, Additions, Deletions,
// ChangedFiles, Mergeable, MergeStateStatus, ReviewRequests, ChecksRollup)
// round-trips through JSON — the acceptance criterion's "a schema test
// asserts the full v3 [now v4] field set."
func TestPR_V4FieldSet_JSONRoundTrip(t *testing.T) {
	in := PR{
		ID:               "pr-1",
		HeadSHA:          "abc123",
		Additions:        10,
		Deletions:        2,
		ChangedFiles:     3,
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewRequests:   []string{"alice", "core-team"},
		ChecksRollup:     "success",
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{
		"head_sha", "additions", "deletions", "changed_files",
		"mergeable", "merge_state_status", "review_requests", "checks_rollup",
	} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("wire JSON missing %q key: %s", key, raw)
		}
	}
}

func TestPRFilesResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(PRFilesResult{
		ID: "pr-1",
		Files: []PRFile{
			{Path: "a.go", Additions: 5, Deletions: 1},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","files":[{"path":"a.go","additions":5,"deletions":1}]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPRFilesResult_JSONShape_EmptyFilesIsEmptyArrayNotNull(t *testing.T) {
	raw, err := json.Marshal(PRFilesResult{ID: "pr-1", Files: []PRFile{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","files":[]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPRCommitsResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(PRCommitsResult{
		ID: "pr-1",
		Commits: []PRCommit{
			{SHA: "abc123", Author: "alice", Message: "fix bug"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","commits":[{"sha":"abc123","author":"alice","message":"fix bug"}]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPRCommitsResult_JSONShape_EmptyCommitsIsEmptyArrayNotNull(t *testing.T) {
	raw, err := json.Marshal(PRCommitsResult{ID: "pr-1", Commits: []PRCommit{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","commits":[]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestPR_BaseSHA_JSONRoundTrip asserts the v6 base_sha field round-trips under
// its exact wire key and is omitted when empty (bead pg2-2j5ac.52.6.2).
func TestPR_BaseSHA_JSONRoundTrip(t *testing.T) {
	in := PR{ID: "pr-1", BaseSHA: "0123456789abcdef0123456789abcdef01234567"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := asMap["base_sha"]; !ok {
		t.Errorf("wire JSON missing base_sha key: %s", raw)
	}

	empty, err := json.Marshal(PR{ID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(empty), "base_sha") {
		t.Errorf("empty BaseSHA must be omitted: %s", empty)
	}
}

// TestPR_ReviewContextFieldsRoundTrip pins the wire names of the review-thread
// id, the thread flags and the per-connection truncation report that `show`
// carries, and that a list-path PR (no report) omits `connections` entirely.
func TestPR_ReviewContextFieldsRoundTrip(t *testing.T) {
	in := PR{
		ID: "pr-1",
		Reviews: []PRReview{{
			ID: "r1",
			Comments: []PRComment{{
				ID: "c2", ThreadID: "c2", ReviewThreadID: "PRRT_x", Resolved: true, ThreadOutdated: true,
			}},
		}},
		Connections: &PRConnections{
			Reviews:  PRConnectionReport{Total: 3, Returned: 3},
			Threads:  PRConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
			Comments: PRConnectionReport{Total: 7, Returned: 7},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"review_thread_id":"PRRT_x"`,
		`"thread_outdated":true`,
		`"thread_id":"c2"`,
		`"threads":{"truncated":true,"total":1001,"returned":1000}`,
		`"reviews":{"truncated":false,"total":3,"returned":3}`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("wire form missing %s in %s", want, raw)
		}
	}
	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}

	listRaw, err := json.Marshal(PR{ID: "pr-2"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(listRaw), "connections") {
		t.Errorf("a PR without a report must omit connections: %s", listRaw)
	}
}
