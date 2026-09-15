package github

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestDecodeCursor_NilMalformedWrongVersion_NeverError proves DecodeCursor
// returns (nil, nil) — never a non-nil error — for a nil/absent cursor,
// malformed JSON, and a wrong-version cursor alike [design: section 4.2].
func TestDecodeCursor_NilMalformedWrongVersion_NeverError(t *testing.T) {
	wrongVersion, err := json.Marshal(FingerprintCursor{Version: fingerprintCursorVersion + 1, ByPR: map[string]string{"o/r#1": "abc"}})
	if err != nil {
		t.Fatalf("marshal wrong-version fixture: %v", err)
	}

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"nil", nil},
		{"empty", json.RawMessage("")},
		{"json null", json.RawMessage("null")},
		{"malformed json", json.RawMessage("{not valid json")},
		{"wrong version", wrongVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, err := DecodeCursor(tc.raw)
			if err != nil {
				t.Fatalf("DecodeCursor(%q) returned a non-nil error: %v", tc.raw, err)
			}
			if cur != nil {
				t.Fatalf("DecodeCursor(%q) = %#v, want nil", tc.raw, cur)
			}
		})
	}
}

// TestDecodeCursor_ValidRoundTrip proves a well-formed, current-version
// cursor decodes back to its original content — the positive control for
// TestDecodeCursor_NilMalformedWrongVersion_NeverError's negative cases.
func TestDecodeCursor_ValidRoundTrip(t *testing.T) {
	want := &FingerprintCursor{Version: fingerprintCursorVersion, ByPR: map[string]string{"o/r#1": "abc123"}}
	raw, err := EncodeCursor(want)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	got, err := DecodeCursor(raw)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if got == nil || !reflect.DeepEqual(*got, *want) {
		t.Fatalf("DecodeCursor(EncodeCursor(want)) = %#v, want %#v", got, want)
	}
}

// TestComputeFingerprint_MatchesPgPrGoldenFixture asserts ComputeFingerprint
// against a GOLDEN FIXTURE literal derived from pg-pr's own real
// fingerprintHash algorithm (packages/pg-pr/internal/sync/detector.go) run
// by hand against this exact synthetic snapshot — never a live call into
// packages/pg-connector/../pg-pr, per this packet's own acceptance-criteria
// clarification (packages/pg-connector and packages/pg-pr are separate Go
// modules with no cross-module dependency, and this test does not add
// one). The composition
// ("%s|%s|%s|%s|%t|%d|%d|%d" over UpdatedAt/HeadOID/StatusRollup/State/
// IsDraft/ReviewCount/CommentCount/ReviewThreadCount, SHA-256'd, first 8
// bytes hex-encoded) was re-ported verbatim from that file; the golden
// value below was computed once, by hand, outside this test suite, via:
//
//	fmt.Sprintf("%s|%s|%s|%s|%t|%d|%d|%d",
//	    "2026-01-02T03:04:05Z", "abc123", "success", "open", false, 2, 3, 1)
//	sha256.Sum256([]byte(s)); hex.EncodeToString(sum[:8])
//
// which produced "07e2dcef6cb42ef7".
func TestComputeFingerprint_MatchesPgPrGoldenFixture(t *testing.T) {
	snapshot := GitHubPRSnapshot{
		ID:                "octocat/hello-world#42",
		UpdatedAt:         "2026-01-02T03:04:05Z",
		HeadOID:           "abc123",
		StatusRollup:      "success",
		State:             "open",
		IsDraft:           false,
		ReviewCount:       2,
		CommentCount:      3,
		ReviewThreadCount: 1,
	}
	const golden = "07e2dcef6cb42ef7"

	got := ComputeFingerprint(snapshot)
	if got != golden {
		t.Fatalf("ComputeFingerprint(%#v) = %q, want golden %q", snapshot, got, golden)
	}

	// Changing any hashed field (ID/identity fields excluded, matching
	// PRFingerprint's own Repo/Number exclusion) must change the hash —
	// otherwise the golden assertion above would be vacuously satisfied by
	// a function that ignores its input.
	changed := snapshot
	changed.ReviewCount++
	if got2 := ComputeFingerprint(changed); got2 == golden {
		t.Fatalf("ComputeFingerprint ignored a changed field: got %q for both the original and a mutated snapshot", got2)
	}
}

// TestRefreshCursor_OneChangedPR proves RefreshCursor, given a previous
// cursor and a live snapshot where exactly one PR's fingerprint changed,
// reports exactly that PR's id as changed and a next cursor reflecting the
// new fingerprint set [design: section 4.2].
func TestRefreshCursor_OneChangedPR(t *testing.T) {
	unchanged := GitHubPRSnapshot{ID: "o/r#1", UpdatedAt: "t1", HeadOID: "h1", State: "open"}
	changedOld := GitHubPRSnapshot{ID: "o/r#2", UpdatedAt: "t1", HeadOID: "h2", State: "open"}
	changedNew := GitHubPRSnapshot{ID: "o/r#2", UpdatedAt: "t2", HeadOID: "h2new", State: "open"}

	prev := &FingerprintCursor{
		Version: fingerprintCursorVersion,
		ByPR: map[string]string{
			unchanged.ID:  ComputeFingerprint(unchanged),
			changedOld.ID: ComputeFingerprint(changedOld),
		},
	}

	live := []GitHubPRSnapshot{unchanged, changedNew}
	gotIDs, next := RefreshCursor(prev, live)

	if want := []string{"o/r#2"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("changedIDs = %#v, want %#v", gotIDs, want)
	}
	if next == nil {
		t.Fatal("next cursor is nil")
	}
	if next.Version != fingerprintCursorVersion {
		t.Fatalf("next.Version = %d, want %d", next.Version, fingerprintCursorVersion)
	}
	wantByPR := map[string]string{
		unchanged.ID:  ComputeFingerprint(unchanged),
		changedNew.ID: ComputeFingerprint(changedNew),
	}
	if !reflect.DeepEqual(next.ByPR, wantByPR) {
		t.Fatalf("next.ByPR = %#v, want %#v", next.ByPR, wantByPR)
	}
}

// TestRefreshCursor_NilPrevIsFullFetch proves RefreshCursor treats a nil
// previous cursor as a full fetch: every live PR is reported changed
// [design: section 5.2, "a backend that ignores cursors and returns
// everything"].
func TestRefreshCursor_NilPrevIsFullFetch(t *testing.T) {
	live := []GitHubPRSnapshot{
		{ID: "o/r#1", State: "open"},
		{ID: "o/r#2", State: "open", IsDraft: true},
		{ID: "o/r#3", State: "closed"},
	}
	gotIDs, next := RefreshCursor(nil, live)

	want := []string{"o/r#1", "o/r#2", "o/r#3"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("changedIDs = %#v, want %#v (every live PR on a full fetch)", gotIDs, want)
	}
	if next == nil || len(next.ByPR) != len(live) {
		t.Fatalf("next cursor = %#v, want one entry per live PR", next)
	}
}

// TestRefreshCursor_NoChanges proves an unchanged live snapshot against its
// own previous cursor reports no changed ids — the negative control
// against TestRefreshCursor_OneChangedPR's positive case.
func TestRefreshCursor_NoChanges(t *testing.T) {
	live := []GitHubPRSnapshot{
		{ID: "o/r#1", State: "open"},
		{ID: "o/r#2", State: "closed"},
	}
	_, first := RefreshCursor(nil, live)
	gotIDs, _ := RefreshCursor(first, live)
	if len(gotIDs) != 0 {
		t.Fatalf("changedIDs = %#v, want none (nothing changed since prev)", gotIDs)
	}
}
