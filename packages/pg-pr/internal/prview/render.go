package prview

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sectionrender"
)

// noneMarker is the human-render text for "this axis's underlying
// collection was asked and reported zero items" — e.g. a store row exists
// but Feedback/Revisions/LinkedTicketKeys/BeadLinks came back empty. It is
// deliberately DIFFERENT text from sectionrender.Unknown ("-", "not known /
// no data source at all"), mirroring the nil-vs-non-nil-empty-slice
// distinction prview.go's package doc already establishes for View's own
// fields: nil means "no data source", a non-nil empty slice means "asked,
// got zero." See TestRenderHuman_UnknownAndNoneMarkersAreDistinct for the
// mechanical check that the two strings never collide.
const noneMarker = "(none)"

// yesNo renders a bool as "yes"/"no", matching the convention
// cmd/pg-pr/pr.go's renderPR already uses for View.Identity.Merged
// ("merged: yes|no").
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// RenderHuman writes v's human-readable rendering to w via
// internal/sectionrender.Render. Write errors are propagated exactly as
// sectionrender.Render propagates them (that package's own tests already
// prove the write-error contract; this function does not re-derive it).
func RenderHuman(w io.Writer, v View) error {
	return sectionrender.Render(w, sections(v)...)
}

// sections builds the full, fixed-length, fixed-order list of
// sectionrender.Section values for v. Split out from RenderHuman so
// render_test.go can inspect the Section values directly (headings,
// fields, details) rather than re-parsing rendered text — in particular
// for the mechanical "same section headings, full vs empty" check.
//
// The list's length and heading order never depend on which axes of v are
// populated: every axis — including the three not-yet-existing ones,
// which Assemble (internal/prview/prview.go) always sets to the
// UnavailableAxis marker regardless of input — contributes exactly one
// Section, always. That is what "no section is silently dropped when its
// data is missing" means structurally for this renderer.
func sections(v View) []sectionrender.Section {
	return []sectionrender.Section{
		identitySection(v),
		ciSection(v.CI),
		mergeStateSection(v.MergeState),
		ownershipSection(v.Ownership),
		enrichmentSection(v.Enrichment),
		feedbackSection(v.Feedback),
		revisionsSection(v.Revisions),
		linkedTicketsSection(v.LinkedTicketKeys),
		beadLinksSection(v.BeadLinks),
		unavailableAxisSection("Approvals", v.Approvals),
		unavailableAxisSection("Policy Bot", v.PolicyBot),
		unavailableAxisSection("Hide/WIP State", v.HideWIP),
	}
}

// identitySection covers View.Identity plus the view-level freshness pair
// (AsOf/Stale) — both are always populated (Identity.PR is a required,
// never-nil input to Assemble; AsOf/Stale are always computed), so this
// section never needs an unknown marker of its own, only per-field
// OrUnknown treatment for individual empty strings (e.g. MergedAt on an
// unmerged PR).
//
// State and Draft are rendered as two SEPARATE fields, deliberately NOT
// collapsed the way cmd/pg-pr/pr.go's renderPR collapses them (Draft &&
// State=="open" -> "draft") for its own human output. prview.go's
// IdentityState doc comment and TestAssemble_PreservesDraftSeparatelyFromState
// record that Assemble keeps State and Draft independently addressable on
// purpose; this render follows that same decision rather than re-collapsing
// it at display time.
func identitySection(v View) sectionrender.Section {
	i := v.Identity
	labels := noneMarker
	if len(i.Labels) > 0 {
		labels = strings.Join(i.Labels, ", ")
	}
	return sectionrender.Section{
		Heading: "PR",
		Fields: []sectionrender.Field{
			{Label: "repo", Value: sectionrender.OrUnknown(i.Repo)},
			{Label: "number", Value: strconv.Itoa(i.Number)},
			{Label: "title", Value: sectionrender.OrUnknown(i.Title)},
			{Label: "state", Value: sectionrender.OrUnknown(i.State)},
			{Label: "draft", Value: yesNo(i.Draft)},
			{Label: "branch", Value: sectionrender.OrUnknown(i.Branch)},
			{Label: "base", Value: sectionrender.OrUnknown(i.Base)},
			{Label: "author", Value: sectionrender.OrUnknown(i.Author)},
			{Label: "url", Value: sectionrender.OrUnknown(i.URL)},
			{Label: "head_sha", Value: sectionrender.OrUnknown(i.HeadSHA)},
			{Label: "base_sha", Value: sectionrender.OrUnknown(i.BaseSHA)},
			{Label: "merged", Value: yesNo(i.Merged)},
			{Label: "merged_at", Value: sectionrender.OrUnknown(i.MergedAt)},
			{Label: "additions", Value: strconv.Itoa(i.Additions)},
			{Label: "deletions", Value: strconv.Itoa(i.Deletions)},
			{Label: "changed_files", Value: strconv.Itoa(i.ChangedFiles)},
			{Label: "labels", Value: labels},
			{Label: "as_of", Value: sectionrender.OrUnknown(v.AsOf)},
			{Label: "stale", Value: yesNo(v.Stale)},
		},
	}
}

// ciSection covers View.CI. CIRollup is never nil and zero runs is a real,
// defined value ("none"/0/0/0 — see cirollup.Compute), not an unknown
// marker, so every field here always has a real value to show.
func ciSection(ci CIRollup) sectionrender.Section {
	return sectionrender.Section{
		Heading: "CI",
		Fields: []sectionrender.Field{
			{Label: "state", Value: sectionrender.OrUnknown(ci.State)},
			{Label: "passed", Value: strconv.Itoa(ci.Passed)},
			{Label: "failed", Value: strconv.Itoa(ci.Failed)},
			{Label: "pending", Value: strconv.Itoa(ci.Pending)},
		},
	}
}

// mergeStateSection covers View.MergeState. Empty Mergeable/MergeStateStatus
// is GitHub's own documented REST-fallback degenerate value (see
// prview.go's MergeState doc comment) rather than a case this axis needs a
// distinct unknown marker for — OrUnknown's "-" is used here purely for
// display, the same way cmd/pg-pr/pr.go's orDash already treats empty
// strings elsewhere in this module.
func mergeStateSection(m MergeState) sectionrender.Section {
	return sectionrender.Section{
		Heading: "Merge State",
		Fields: []sectionrender.Field{
			{Label: "mergeable", Value: sectionrender.OrUnknown(m.Mergeable)},
			{Label: "merge_state_status", Value: sectionrender.OrUnknown(m.MergeStateStatus)},
			{Label: "auto_merge_enabled", Value: yesNo(m.AutoMergeEnabled)},
			{Label: "has_conflict", Value: yesNo(m.HasConflict)},
		},
	}
}

// ownershipSection covers View.Ownership (*string). A nil pointer means "no
// store row exists yet" — treated as the empty string and run through
// OrUnknown, which yields the same "-" marker a store row's own empty
// Ownership column would (both are simply "not known right now" for a human
// reader; only the JSON contract sibling bead needs the finer null-vs-empty
// distinction on the wire).
func ownershipSection(o *string) sectionrender.Section {
	v := ""
	if o != nil {
		v = *o
	}
	return sectionrender.Section{
		Heading: "Ownership",
		Fields: []sectionrender.Field{
			{Label: "ownership", Value: sectionrender.OrUnknown(v)},
		},
	}
}

// enrichmentSection covers View.Enrichment (*Enrichment). A nil pointer
// (no store row) is treated as the zero-value Enrichment, so every field
// renders through OrUnknown uniformly — nil and "store row present but
// this field empty" render identically, same rationale as ownershipSection.
func enrichmentSection(e *Enrichment) sectionrender.Section {
	var v Enrichment
	if e != nil {
		v = *e
	}
	urgency := sectionrender.OrUnknown(v.Urgency)
	if len(v.UrgencyReasons) > 0 {
		urgency = fmt.Sprintf("%s (%s)", urgency, strings.Join(v.UrgencyReasons, ", "))
	}
	return sectionrender.Section{
		Heading: "Enrichment",
		Fields: []sectionrender.Field{
			{Label: "kind", Value: sectionrender.OrUnknown(v.Kind)},
			{Label: "size", Value: sectionrender.OrUnknown(v.Size)},
			{Label: "languages", Value: sectionrender.OrUnknown(strings.Join(v.Languages, ", "))},
			{Label: "urgency", Value: urgency},
		},
	}
}

// listDetails renders the Details lines for a collection axis that carries
// the nil-vs-non-nil-empty distinction (Feedback, Revisions,
// LinkedTicketKeys, BeadLinks — see prview.go's PRViewInput doc comment):
// nil ("no data source at all") -> a single sectionrender.Unknown line;
// non-nil empty ("asked, got zero") -> a single noneMarker line; otherwise
// one rendered line per item via toLine, in order.
func listDetails[T any](items []T, toLine func(T) string) []string {
	if items == nil {
		return []string{sectionrender.Unknown}
	}
	if len(items) == 0 {
		return []string{noneMarker}
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, toLine(it))
	}
	return out
}

func feedbackSection(items []FeedbackItem) sectionrender.Section {
	return sectionrender.Section{
		Heading: "Feedback",
		Details: listDetails(items, func(f FeedbackItem) string {
			return fmt.Sprintf("#%d [%s] %s (author=%s, status=%s, severity=%s)",
				f.ID, sectionrender.OrUnknown(f.Kind), sectionrender.OrUnknown(f.Title),
				sectionrender.OrUnknown(f.AuthorLogin), sectionrender.OrUnknown(f.Status),
				sectionrender.OrUnknown(f.Severity))
		}),
	}
}

func revisionsSection(items []RevisionItem) sectionrender.Section {
	return sectionrender.Section{
		Heading: "Revisions",
		Details: listDetails(items, func(r RevisionItem) string {
			return fmt.Sprintf("seq=%d head=%s ci=%s(passed=%d,failed=%d,pending=%d) gate=%s(%d/%d)",
				r.Seq, sectionrender.OrUnknown(r.HeadSHA), sectionrender.OrUnknown(r.CIState),
				r.CIPassed, r.CIFailed, r.CIPending, sectionrender.OrUnknown(r.GateState),
				r.GateStateN, r.GateStateM)
		}),
	}
}

func linkedTicketsSection(keys []string) sectionrender.Section {
	return sectionrender.Section{
		Heading: "Linked Tickets",
		Details: listDetails(keys, func(k string) string { return k }),
	}
}

func beadLinksSection(items []BeadLinkItem) sectionrender.Section {
	return sectionrender.Section{
		Heading: "Bead Links",
		Details: listDetails(items, func(d BeadLinkItem) string {
			return fmt.Sprintf("%s [%s] %s (%s)",
				d.ID, sectionrender.OrUnknown(d.Status), sectionrender.OrUnknown(d.Title), d.URL)
		}),
	}
}

// unavailableAxisSection covers one of the three not-yet-existing axes
// (Approvals, PolicyBot, HideWIP). Per this bead's carried-forward ruling,
// it renders the SAME unknown marker any other genuinely-absent-but-
// existing axis uses (sectionrender.Unknown), never the UnavailableAxis's
// own Reason string: a human reader of this render does not need — and per
// the ruling must not be given — a way to distinguish "this feature has
// literally never been implemented" from "this specific PR has no value
// for an axis that does exist." Both read as "-" here. The axis param is
// still threaded through (rather than hardcoding "-") so a future axis that
// legitimately becomes available can be rendered from here without
// reshaping the call sites — Assemble never sets Available true today (see
// prview.go's unavailable() helper), so the true branch is unreached but
// kept for that reason.
func unavailableAxisSection(heading string, axis UnavailableAxis) sectionrender.Section {
	status := sectionrender.Unknown
	if axis.Available {
		status = "available"
	}
	return sectionrender.Section{
		Heading: heading,
		Fields: []sectionrender.Field{
			{Label: "status", Value: status},
		},
	}
}
