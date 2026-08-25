package prview

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fixedNow is the clock every test threads through PRViewInput.Now, so no
// test depends on the wall clock. Chosen well after any fixture's as-of time.
var fixedNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// 1. Every EXISTING axis maps into View correctly: one populated case, one
//    absent case, each.
// ---------------------------------------------------------------------------

func TestAssemble_Identity_Populated(t *testing.T) {
	in := PRViewInput{
		PR: api.PR{
			Repo: "o/r", Number: 42, Title: "add feature", Body: "This adds a feature.", State: "open",
			Branch: "alice/add-feature", Base: "main", Author: "alice",
			URL: "https://example.invalid/o/r/pull/42", HeadSHA: "head1", BaseSHA: "base1",
			Additions: 10, Deletions: 4, ChangedFiles: 3,
			Labels: []string{"lbl-one"},
		},
		Now: fixedNow,
	}
	got := Assemble(in).Identity
	want := IdentityState{
		Repo: "o/r", Number: 42, Title: "add feature", Body: "This adds a feature.", State: "open",
		Branch: "alice/add-feature", Base: "main", Author: "alice",
		URL: "https://example.invalid/o/r/pull/42", HeadSHA: "head1", BaseSHA: "base1",
		Additions: 10, Deletions: 4, ChangedFiles: 3,
		Labels: []string{"lbl-one"},
	}
	if !identityEqual(got, want) {
		t.Fatalf("Identity = %+v, want %+v", got, want)
	}
}

func TestAssemble_Identity_Absent(t *testing.T) {
	// The "absent" case for this axis: PR is required (never a pointer, per
	// PRInput precedent) so there is no missing-input form — the degenerate
	// case is the zero-value api.PR{}, which must map to a zero-value
	// IdentityState without panicking.
	got := Assemble(PRViewInput{Now: fixedNow}).Identity
	want := IdentityState{}
	if !identityEqual(got, want) {
		t.Fatalf("Identity = %+v, want zero value %+v", got, want)
	}
}

func identityEqual(a, b IdentityState) bool {
	if a.Repo != b.Repo || a.Number != b.Number || a.Title != b.Title || a.Body != b.Body || a.State != b.State ||
		a.Draft != b.Draft || a.Branch != b.Branch || a.Base != b.Base || a.Author != b.Author ||
		a.URL != b.URL || a.HeadSHA != b.HeadSHA || a.BaseSHA != b.BaseSHA || a.Merged != b.Merged ||
		a.MergedAt != b.MergedAt || a.Additions != b.Additions || a.Deletions != b.Deletions ||
		a.ChangedFiles != b.ChangedFiles || len(a.Labels) != len(b.Labels) {
		return false
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
}

func TestAssemble_Ownership_Populated(t *testing.T) {
	in := PRViewInput{
		Store: &store.PullRequest{Repo: "o/r", Number: 42, Ownership: "mine"},
		Now:   fixedNow,
	}
	got := Assemble(in).Ownership
	if got == nil || *got != "mine" {
		t.Fatalf("Ownership = %v, want pointer to %q", got, "mine")
	}
}

func TestAssemble_Ownership_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).Ownership
	if got != nil {
		t.Fatalf("Ownership = %v, want nil (no store row)", *got)
	}
}

func TestAssemble_Enrichment_Populated(t *testing.T) {
	in := PRViewInput{
		Store: &store.PullRequest{
			Repo: "o/r", Number: 42,
			Kind: "feature", Size: "M", Languages: []string{"go", "nix"},
			Urgency: "high", UrgencyScore: 7, UrgencyReasons: []string{"incident-linked"},
		},
		Now: fixedNow,
	}
	got := Assemble(in).Enrichment
	if got == nil {
		t.Fatalf("Enrichment = nil, want populated")
	}
	want := Enrichment{
		Kind: "feature", Size: "M", Languages: []string{"go", "nix"},
		Urgency: "high", UrgencyScore: 7, UrgencyReasons: []string{"incident-linked"},
	}
	if got.Kind != want.Kind || got.Size != want.Size || got.Urgency != want.Urgency ||
		got.UrgencyScore != want.UrgencyScore || !strSliceEqual(got.Languages, want.Languages) ||
		!strSliceEqual(got.UrgencyReasons, want.UrgencyReasons) {
		t.Fatalf("Enrichment = %+v, want %+v", *got, want)
	}
}

func TestAssemble_Enrichment_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).Enrichment
	if got != nil {
		t.Fatalf("Enrichment = %+v, want nil (no store row)", *got)
	}
}

func TestAssemble_CIRollup_Populated(t *testing.T) {
	in := PRViewInput{
		CIRuns: []api.CIRun{
			{Name: "unit", Status: "completed", Conclusion: "success"},
			{Name: "lint", Status: "completed", Conclusion: "failure"},
		},
		Now: fixedNow,
	}
	got := Assemble(in).CI
	want := CIRollup{State: "failure", Passed: 1, Failed: 1, Pending: 0}
	if got != want {
		t.Fatalf("CI = %+v, want %+v", got, want)
	}
}

func TestAssemble_CIRollup_Absent(t *testing.T) {
	// No CI runs is a real, defined value ("none"/0/0/0) — see cirollup.Compute
	// — not an unknown marker; this is the axis's degenerate case.
	got := Assemble(PRViewInput{Now: fixedNow}).CI
	want := CIRollup{State: "none"}
	if got != want {
		t.Fatalf("CI = %+v, want %+v", got, want)
	}
}

func TestAssemble_MergeState_Populated(t *testing.T) {
	in := PRViewInput{
		PR: api.PR{
			Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", AutoMergeEnabled: true,
		},
		Now: fixedNow,
	}
	got := Assemble(in).MergeState
	want := MergeState{Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", AutoMergeEnabled: true, HasConflict: true}
	if got != want {
		t.Fatalf("MergeState = %+v, want %+v", got, want)
	}
}

func TestAssemble_MergeState_Absent(t *testing.T) {
	// Empty Mergeable/MergeStateStatus is GitHub's own documented REST-fallback
	// degenerate value (pkg/api/pr.go), not something this axis needs a
	// separate unknown marker for.
	got := Assemble(PRViewInput{Now: fixedNow}).MergeState
	want := MergeState{}
	if got != want {
		t.Fatalf("MergeState = %+v, want %+v", got, want)
	}
}

func TestAssemble_Feedback_Populated(t *testing.T) {
	in := PRViewInput{
		Feedback: []store.Feedback{
			{ID: 1, Kind: "ci-failure", Status: "new", Title: "unit failed", AuthorLogin: "bob"},
		},
		Now: fixedNow,
	}
	got := Assemble(in).Feedback
	if len(got) != 1 || got[0].ID != 1 || got[0].Kind != "ci-failure" || got[0].AuthorLogin != "bob" {
		t.Fatalf("Feedback = %+v, want one ci-failure item from bob", got)
	}
}

func TestAssemble_Feedback_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).Feedback
	if got != nil {
		t.Fatalf("Feedback = %+v, want nil (no store data)", got)
	}
}

func TestAssemble_Revisions_Populated(t *testing.T) {
	in := PRViewInput{
		Revisions: []store.Revision{
			{Seq: 1, HeadSHA: "h1", CIState: "success", GateState: "satisfied"},
			{Seq: 2, HeadSHA: "h2", CIState: "pending", GateState: "unknown"},
		},
		Now: fixedNow,
	}
	got := Assemble(in).Revisions
	if len(got) != 2 || got[0].Seq != 1 || got[1].HeadSHA != "h2" {
		t.Fatalf("Revisions = %+v, want two revisions in seq order", got)
	}
}

func TestAssemble_Revisions_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).Revisions
	if got != nil {
		t.Fatalf("Revisions = %+v, want nil (no store data)", got)
	}
}

func TestAssemble_LinkedTicketKeys_Populated(t *testing.T) {
	in := PRViewInput{LinkedTicketKeys: []string{"ABC-123", "ABC-124"}, Now: fixedNow}
	got := Assemble(in).LinkedTicketKeys
	if !strSliceEqual(got, []string{"ABC-123", "ABC-124"}) {
		t.Fatalf("LinkedTicketKeys = %v, want [ABC-123 ABC-124]", got)
	}
}

func TestAssemble_LinkedTicketKeys_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).LinkedTicketKeys
	if got != nil {
		t.Fatalf("LinkedTicketKeys = %v, want nil (none extracted)", got)
	}
}

func TestAssemble_BeadLinks_Populated(t *testing.T) {
	in := PRViewInput{
		BeadLinks: []beads.DepNode{
			{ID: "pg2-abc12", Title: "fix thing", Status: "open", Labels: []string{"human"}},
		},
		Now: fixedNow,
	}
	got := Assemble(in).BeadLinks
	if len(got) != 1 || got[0].ID != "pg2-abc12" || got[0].URL != "bd://pg2-abc12" {
		t.Fatalf("BeadLinks = %+v, want one bd://pg2-abc12 item", got)
	}
}

func TestAssemble_BeadLinks_Absent(t *testing.T) {
	got := Assemble(PRViewInput{Now: fixedNow}).BeadLinks
	if got != nil {
		t.Fatalf("BeadLinks = %+v, want nil (no dep tree data)", got)
	}
}

func strSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 2. The three NOT-YET-EXISTING axes always render the explicit unknown
//    marker — one fixed-behavior assertion per axis.
// ---------------------------------------------------------------------------

func TestAssemble_ApprovalsAxisAlwaysUnavailable(t *testing.T) {
	for _, in := range []PRViewInput{
		{Now: fixedNow},
		{Now: fixedNow, PR: api.PR{Repo: "o/r", Number: 42}, Store: &store.PullRequest{Ownership: "mine"}},
	} {
		got := Assemble(in).Approvals
		want := UnavailableAxis{Available: false, Reason: AxisApprovals}
		if got != want {
			t.Fatalf("Approvals = %+v, want %+v", got, want)
		}
	}
}

func TestAssemble_PolicyBotAxisAlwaysUnavailable(t *testing.T) {
	for _, in := range []PRViewInput{
		{Now: fixedNow},
		{Now: fixedNow, PR: api.PR{Repo: "o/r", Number: 42}, Store: &store.PullRequest{Ownership: "team"}},
	} {
		got := Assemble(in).PolicyBot
		want := UnavailableAxis{Available: false, Reason: AxisPolicyBot}
		if got != want {
			t.Fatalf("PolicyBot = %+v, want %+v", got, want)
		}
	}
}

func TestAssemble_HideWIPAxisAlwaysUnavailable(t *testing.T) {
	for _, in := range []PRViewInput{
		{Now: fixedNow},
		{Now: fixedNow, PR: api.PR{Repo: "o/r", Number: 42, Draft: true}},
	} {
		got := Assemble(in).HideWIP
		want := UnavailableAxis{Available: false, Reason: AxisHideWIP}
		if got != want {
			t.Fatalf("HideWIP = %+v, want %+v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Ownership/draft-state derivation: Assemble deliberately does NOT apply
//    cmd/pg-pr/pr.go's renderPR draft-collapse (Draft && State=="open" ->
//    State="draft"). State and Draft stay independently addressable.
// ---------------------------------------------------------------------------

func TestAssemble_PreservesDraftSeparatelyFromState(t *testing.T) {
	in := PRViewInput{
		PR:  api.PR{Repo: "o/r", Number: 42, State: "open", Draft: true},
		Now: fixedNow,
	}
	got := Assemble(in).Identity
	if got.State != "open" {
		t.Errorf("State = %q, want the raw provider state %q (not collapsed to \"draft\")", got.State, "open")
	}
	if !got.Draft {
		t.Errorf("Draft = false, want true")
	}
}

// ---------------------------------------------------------------------------
// 4. Freshness: fresh, aged past bound, empty/unparseable as-of (must report
//    stale per INV-ASOF-1).
// ---------------------------------------------------------------------------

func TestAssemble_Freshness_Fresh(t *testing.T) {
	in := PRViewInput{
		Store: &store.PullRequest{LastSyncedAt: fixedNow.Add(-30 * time.Second).Format(time.RFC3339)},
		Now:   fixedNow,
	}
	view := Assemble(in)
	if view.Stale {
		t.Errorf("Stale = true, want false for a recently-synced row")
	}
	if view.AsOf == "" {
		t.Errorf("AsOf = %q, want the store's last_synced_at verbatim", view.AsOf)
	}
}

func TestAssemble_Freshness_AgedPastBound(t *testing.T) {
	// freshness.BoundSeconds(0) falls back to DefaultSyncIntervalSeconds*2 =
	// 120s; well past that is definitely stale.
	in := PRViewInput{
		Store: &store.PullRequest{LastSyncedAt: fixedNow.Add(-1 * time.Hour).Format(time.RFC3339)},
		Now:   fixedNow,
	}
	if !Assemble(in).Stale {
		t.Errorf("Stale = false, want true for a row synced an hour ago")
	}
}

func TestAssemble_Freshness_NoStoreRowIsStale(t *testing.T) {
	view := Assemble(PRViewInput{Now: fixedNow})
	if !view.Stale {
		t.Errorf("Stale = false, want true when there is no store row (no usable as-of time)")
	}
	if view.AsOf != "" {
		t.Errorf("AsOf = %q, want empty when there is no store row", view.AsOf)
	}
}

func TestAssemble_Freshness_UnparseableAsOfIsStale(t *testing.T) {
	in := PRViewInput{
		Store: &store.PullRequest{LastSyncedAt: "not-a-timestamp"},
		Now:   fixedNow,
	}
	view := Assemble(in)
	if !view.Stale {
		t.Errorf("Stale = false, want true for an unparseable as-of time (fail closed)")
	}
	if view.AsOf != "not-a-timestamp" {
		t.Errorf("AsOf = %q, want the raw store value passed through verbatim", view.AsOf)
	}
}

// ---------------------------------------------------------------------------
// 5. A nil/zero-value api.PR input must not panic and must produce a View
//    with every optional field at its unknown marker.
// ---------------------------------------------------------------------------

func TestAssemble_ZeroValueInputDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Assemble(PRViewInput{}) panicked: %v", r)
		}
	}()
	got := Assemble(PRViewInput{})

	if !identityEqual(got.Identity, IdentityState{}) {
		t.Errorf("Identity = %+v, want zero value", got.Identity)
	}
	if got.Ownership != nil {
		t.Errorf("Ownership = %v, want nil", *got.Ownership)
	}
	if got.Enrichment != nil {
		t.Errorf("Enrichment = %+v, want nil", *got.Enrichment)
	}
	if got.Feedback != nil {
		t.Errorf("Feedback = %+v, want nil", got.Feedback)
	}
	if got.Revisions != nil {
		t.Errorf("Revisions = %+v, want nil", got.Revisions)
	}
	if got.LinkedTicketKeys != nil {
		t.Errorf("LinkedTicketKeys = %v, want nil", got.LinkedTicketKeys)
	}
	if got.BeadLinks != nil {
		t.Errorf("BeadLinks = %+v, want nil", got.BeadLinks)
	}
	wantUnavail := func(got UnavailableAxis, reason string) {
		if got != (UnavailableAxis{Available: false, Reason: reason}) {
			t.Errorf("axis marker = %+v, want unavailable(%q)", got, reason)
		}
	}
	wantUnavail(got.Approvals, AxisApprovals)
	wantUnavail(got.PolicyBot, AxisPolicyBot)
	wantUnavail(got.HideWIP, AxisHideWIP)
	// A zero-value Now with no store row still resolves to stale (fail
	// closed) rather than panicking on the freshness computation.
	if !got.Stale {
		t.Errorf("Stale = false, want true")
	}
}

// ---------------------------------------------------------------------------
// 6. No store row present: Assemble still produces a View from the
//    provider-derived input alone; store-backed sections carry no data; never
//    an error (Assemble has no error return at all).
// ---------------------------------------------------------------------------

func TestAssemble_NoStoreRow_BuildsFromProviderInputAlone(t *testing.T) {
	in := PRViewInput{
		PR: api.PR{
			Repo: "o/r", Number: 42, Title: "add feature", State: "open", Author: "alice",
			Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		},
		CIRuns: []api.CIRun{{Name: "unit", Status: "completed", Conclusion: "success"}},
		Now:    fixedNow,
	}
	got := Assemble(in)

	// Provider-derived sections are populated even with no store row.
	if got.Identity.Repo != "o/r" || got.Identity.Number != 42 || got.Identity.Author != "alice" {
		t.Errorf("Identity = %+v, want provider-derived fields present", got.Identity)
	}
	if got.MergeState.Mergeable != "MERGEABLE" {
		t.Errorf("MergeState = %+v, want provider-derived Mergeable present", got.MergeState)
	}
	if got.CI.State != "success" {
		t.Errorf("CI = %+v, want provider-derived rollup computed", got.CI)
	}

	// Store-backed sections carry no data — nil, not an error, not a zero
	// (but non-nil) struct that would misleadingly look like "known empty".
	if got.Ownership != nil {
		t.Errorf("Ownership = %v, want nil", *got.Ownership)
	}
	if got.Enrichment != nil {
		t.Errorf("Enrichment = %+v, want nil", *got.Enrichment)
	}
	if got.Feedback != nil || got.Revisions != nil {
		t.Errorf("Feedback/Revisions = %+v/%+v, want nil/nil", got.Feedback, got.Revisions)
	}
}

// ---------------------------------------------------------------------------
// 7. Store row present with every optional column empty: every section
//    carries its unknown marker; NO section is silently dropped from View.
// ---------------------------------------------------------------------------

func TestAssemble_StoreRowWithEmptyOptionalColumns_NoSectionDropped(t *testing.T) {
	in := PRViewInput{
		PR:    api.PR{Repo: "o/r", Number: 42},
		Store: &store.PullRequest{Repo: "o/r", Number: 42}, // every optional column left at its zero value
		Now:   fixedNow,
	}
	got := Assemble(in)

	// The row is PRESENT, so Ownership/Enrichment must be non-nil (known,
	// just empty) — the opposite of the no-store-row case above. Asserted on
	// the struct/fields directly, per the bead's testing plan (never on
	// rendered text — there is none in this package).
	if got.Ownership == nil {
		t.Fatalf("Ownership = nil, want a non-nil pointer (store row is present, even if empty)")
	}
	if *got.Ownership != "" {
		t.Errorf("Ownership = %q, want empty string (store row's own zero value)", *got.Ownership)
	}
	if got.Enrichment == nil {
		t.Fatalf("Enrichment = nil, want a non-nil pointer (store row is present, even if empty)")
	}
	if e := *got.Enrichment; e.Kind != "" || e.Size != "" || e.Urgency != "" || e.UrgencyScore != 0 ||
		e.Languages != nil || e.UrgencyReasons != nil {
		t.Errorf("Enrichment = %+v, want zero value (no fields dropped)", e)
	}
	// Every other section is still present on the struct (Go's zero value
	// for a struct field is never "absent" — this pins that no section was
	// forgotten/omitted from View's definition).
	if got.CI != (CIRollup{State: "none"}) {
		t.Errorf("CI = %+v, want the defined empty rollup", got.CI)
	}
	if got.MergeState != (MergeState{}) {
		t.Errorf("MergeState = %+v, want zero value", got.MergeState)
	}
}

// ---------------------------------------------------------------------------
// 8. Zero-length collections: View's slice fields are present and empty, and
//    that emptiness is TYPE-DISTINGUISHABLE from "unknown" (nil vs non-nil
//    empty).
// ---------------------------------------------------------------------------

func TestAssemble_EmptyCollectionsAreDistinguishableFromUnknown(t *testing.T) {
	in := PRViewInput{
		Feedback:         []store.Feedback{},
		Revisions:        []store.Revision{},
		LinkedTicketKeys: []string{},
		BeadLinks:        []beads.DepNode{},
		Now:              fixedNow,
	}
	got := Assemble(in)

	if got.Feedback == nil {
		t.Errorf("Feedback = nil, want a non-nil empty slice (store was asked, reported zero)")
	}
	if len(got.Feedback) != 0 {
		t.Errorf("Feedback = %+v, want empty", got.Feedback)
	}
	if got.Revisions == nil {
		t.Errorf("Revisions = nil, want a non-nil empty slice")
	}
	if got.LinkedTicketKeys == nil {
		t.Errorf("LinkedTicketKeys = nil, want a non-nil empty slice")
	}
	if got.BeadLinks == nil {
		t.Errorf("BeadLinks = nil, want a non-nil empty slice")
	}

	// Contrast: the SAME fields with a nil (never-asked) input must stay nil
	// — the two are different Go values (and different JSON: null vs []).
	unknown := Assemble(PRViewInput{Now: fixedNow})
	if unknown.Feedback != nil || unknown.Revisions != nil || unknown.LinkedTicketKeys != nil || unknown.BeadLinks != nil {
		t.Errorf("nil-input collections must stay nil: feedback=%v revisions=%v tickets=%v beads=%v",
			unknown.Feedback, unknown.Revisions, unknown.LinkedTicketKeys, unknown.BeadLinks)
	}
}
