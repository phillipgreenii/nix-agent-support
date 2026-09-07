package internal

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func newMigrateTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "store.json"))
}

func TestImportLegacyDispositions_MapsRecognisedKinds(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_1", DispositionAction: "will-fix"},
		{Kind: "pr-comments", CommentNodeID: "IC_2", DispositionAction: "wont-fix"},
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_3", DispositionAction: "no-action"},
	}
	n, err := ImportLegacyDispositions(s, "owner/repo#1", items)
	if err != nil {
		t.Fatalf("ImportLegacyDispositions: %v", err)
	}
	if n != 3 {
		t.Fatalf("imported = %d, want 3", n)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := map[string]schema.Disposition{
		"PRRC_1": schema.DispositionWillFix,
		"IC_2":   schema.DispositionWontFix,
		"PRRC_3": schema.DispositionNoAction,
	}
	for id, disp := range want {
		if got := st.Dispositions[id]; got != disp {
			t.Errorf("Dispositions[%s] = %q, want %q", id, got, disp)
		}
	}
}

func TestImportLegacyDispositions_SkipsNonCommentKinds(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "ci-failure", CommentNodeID: "RUN_1", DispositionAction: "will-fix"},
		{Kind: "review-request", CommentNodeID: "REV_1", DispositionAction: "will-fix"},
		{Kind: "self-review", CommentNodeID: "SR_1", DispositionAction: "will-fix"},
	}
	n, err := ImportLegacyDispositions(s, "owner/repo#1", items)
	if err != nil {
		t.Fatalf("ImportLegacyDispositions: %v", err)
	}
	if n != 0 {
		t.Fatalf("imported = %d, want 0 — non-comment kinds have no commentID slot in this store", n)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(st.Dispositions) != 0 {
		t.Fatalf("Dispositions = %+v, want empty", st.Dispositions)
	}
}

func TestImportLegacyDispositions_SkipsNeverDispositionedItems(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_1", DispositionAction: ""},
	}
	n, err := ImportLegacyDispositions(s, "owner/repo#1", items)
	if err != nil {
		t.Fatalf("ImportLegacyDispositions: %v", err)
	}
	if n != 0 {
		t.Fatalf("imported = %d, want 0", n)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := st.Dispositions["PRRC_1"]; ok {
		t.Fatalf("an undispositioned legacy row must not be imported as an explicit disposition (would manufacture history pg-pr never recorded): %+v", st.Dispositions)
	}
}

func TestImportLegacyDispositions_PrefersCommentNodeIDOverExternalID(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_1", ExternalID: "legacy-external-1", DispositionAction: "will-fix"},
	}
	if _, err := ImportLegacyDispositions(s, "owner/repo#1", items); err != nil {
		t.Fatalf("ImportLegacyDispositions: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := st.Dispositions["PRRC_1"]; !ok {
		t.Fatalf("expected the CommentNodeID to be used as the key: %+v", st.Dispositions)
	}
	if _, ok := st.Dispositions["legacy-external-1"]; ok {
		t.Fatalf("ExternalID must not be used when CommentNodeID is present: %+v", st.Dispositions)
	}
}

func TestImportLegacyDispositions_FallsBackToExternalID(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "pr-comments", ExternalID: "legacy-external-2", DispositionAction: "no-action"},
	}
	if _, err := ImportLegacyDispositions(s, "owner/repo#1", items); err != nil {
		t.Fatalf("ImportLegacyDispositions: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Dispositions["legacy-external-2"] != schema.DispositionNoAction {
		t.Fatalf("Dispositions = %+v, want legacy-external-2 -> no-action", st.Dispositions)
	}
}

func TestImportLegacyDispositions_UnrecognisedActionIsReportedNotAborted(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_1", DispositionAction: "bogus-action"},
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_2", DispositionAction: "will-fix"},
	}
	n, err := ImportLegacyDispositions(s, "owner/repo#1", items)
	if err == nil {
		t.Fatal("expected an error reporting the unrecognised DispositionAction")
	}
	if !strings.Contains(err.Error(), "bogus-action") {
		t.Fatalf("err = %v, want it to name the unrecognised value", err)
	}
	if n != 1 {
		t.Fatalf("imported = %d, want 1 — one bad row must not abort the other valid ones", n)
	}
	st, getErr := s.Get("owner/repo#1")
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if st.Dispositions["PRRC_2"] != schema.DispositionWillFix {
		t.Fatalf("Dispositions = %+v, want PRRC_2 imported despite the sibling error", st.Dispositions)
	}
}

func TestImportLegacyDispositions_IdempotentReimport(t *testing.T) {
	s := newMigrateTestStore(t)
	items := []LegacyFeedbackItem{
		{Kind: "code-comment-thread", CommentNodeID: "PRRC_1", DispositionAction: "will-fix"},
	}
	if _, err := ImportLegacyDispositions(s, "owner/repo#1", items); err != nil {
		t.Fatalf("first import: %v", err)
	}
	n, err := ImportLegacyDispositions(s, "owner/repo#1", items)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-import count = %d, want 1 (idempotent set/overwrite, not an error or a skip)", n)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(st.Dispositions) != 1 || st.Dispositions["PRRC_1"] != schema.DispositionWillFix {
		t.Fatalf("Dispositions = %+v, want exactly one PRRC_1 -> will-fix (re-import must not duplicate)", st.Dispositions)
	}
}

// TestLegacyFeedbackItem_DecodesPgPrFeedbackListJSONShape proves
// LegacyFeedbackItem actually decodes the JSON shape `pg-pr feedback list
// --json` really emits — a []store.Feedback value marshaled with no json
// tags, so its keys are the exported Go field names verbatim, including
// every field this migration does not care about (which must be ignored,
// not rejected).
func TestLegacyFeedbackItem_DecodesPgPrFeedbackListJSONShape(t *testing.T) {
	// A representative excerpt of one real pg-pr store.Feedback row's JSON
	// encoding — every exported field pg-pr's struct has, most irrelevant
	// to this migration, to prove unknown fields are tolerated.
	raw := `{
		"ID": 42,
		"PRID": 7,
		"Kind": "code-comment-thread",
		"ExternalID": "",
		"Fingerprint": "abc123",
		"Status": "dispositioned",
		"Title": "nit: rename this",
		"Body": "please rename x to y",
		"AuthorLogin": "reviewer1",
		"AuthorKind": "human",
		"DispositionAction": "will-fix",
		"DispositionNote": "agreed",
		"File": "main.go",
		"Line": 10,
		"CommentNodeID": "PRRC_kwDOabc123"
	}`
	var item LegacyFeedbackItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if item.Kind != "code-comment-thread" || item.CommentNodeID != "PRRC_kwDOabc123" || item.DispositionAction != "will-fix" {
		t.Fatalf("decoded = %+v", item)
	}
}
