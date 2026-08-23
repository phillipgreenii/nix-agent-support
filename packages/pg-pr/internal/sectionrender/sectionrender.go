// Package sectionrender provides a small, DATA-AGNOSTIC convention for
// rendering multi-section human-readable output: a heading line per
// section, followed by its indented fields and/or detail lines.
//
// It generalizes the shape already used ad hoc by
// cmd/pg-pr/sync_duplicates.go's renderDuplicateReports (a header line per
// group, indented detail lines per item) into a reusable form so a future
// command does not have to hand-roll its own fmt.Fprintf sequence the way
// every existing pg-pr renderer does (renderPR, renderEnrichment,
// renderFiles, renderCommits, renderDuplicateReports, ...).
//
// This package has NO knowledge of pg-pr's domain: no api.PR, no
// store.*, no internal/prview.View. A caller maps its own domain data into
// Section/Field values and hands them to Render.
package sectionrender

import (
	"fmt"
	"io"
)

// Unknown is the marker text for "value not known / not applicable". Every
// caller SHOULD use it (directly, or via OrUnknown) so every command prints
// the same placeholder rather than each choosing its own dash, em-dash, or
// "n/a".
//
// This is the exported equivalent of the LOCAL convention already used by
// cmd/pg-pr/pr.go's orDash helper; that helper is left as-is (it has its
// own callers in that file), but future callers — including other
// commands and the single-PR view — should import Unknown/OrUnknown from
// here instead of re-inventing the marker in a fourth place.
const Unknown = "-"

// OrUnknown returns s, or Unknown when s is empty.
func OrUnknown(s string) string {
	if s == "" {
		return Unknown
	}
	return s
}

// Field is one label/value pair rendered as an indented detail line under
// a Section's heading, as "  <Label>: <Value>".
type Field struct {
	Label string
	Value string
}

// Section is a heading plus an ordered list of Fields and/or free-form
// Details, rendered in the header/indented-detail shape of
// cmd/pg-pr/sync_duplicates.go's renderDuplicateReports:
//
//	<Heading>
//	  <Field.Label>: <Field.Value>
//	  ...
//	  <Detail line>
//	  ...
//
// A Section with zero Fields and zero Details still renders its Heading
// line — a Section is never silently dropped. This is the structural
// property a caller's "no section silently dropped" acceptance criterion
// can rely on: the number of Sections handed to Render is exactly the
// number of heading lines that appear in the output.
type Section struct {
	// Heading is the section's header line, written with no indent.
	Heading string
	// Fields are label/value pairs, one per line, indented two spaces,
	// as "  <Label>: <Value>". Rendered before Details.
	Fields []Field
	// Details are free-form lines written after Fields, indented the
	// same two spaces, verbatim (no "<label>: " prefix). Use this for
	// nested list items a Field's single label/value shape doesn't fit
	// (e.g. renderDuplicateReports's per-duplicate-group lines).
	Details []string
}

// Separator is the exact text Render writes BETWEEN two consecutive
// Sections (never before the first or after the last): a single blank
// line. Pinned here — and by TestRender_MultipleSections — because a
// caller's golden output will depend on it byte-for-byte.
const Separator = "\n"

// Render writes sections to w in the header/indented-detail shape
// documented on Section, in call order, with Separator written between
// (not before the first, not after the last) each pair of sections.
//
// Write errors ARE propagated to the caller. This is a deliberate choice:
// cmd/pg-pr/sync_duplicates.go's renderDuplicateReports swallows write
// errors with the comment "non-fatal — the caller may have closed the
// writer", but that judgment call belongs to a specific caller with a
// specific writer lifetime, not to this generic package. Render reports
// the error and lets each caller decide for itself whether to ignore it
// (as renderDuplicateReports's callers already choose to) or act on it.
func Render(w io.Writer, sections ...Section) error {
	for i, s := range sections {
		if i > 0 {
			if _, err := io.WriteString(w, Separator); err != nil {
				return err
			}
		}
		if err := renderSection(w, s); err != nil {
			return err
		}
	}
	return nil
}

func renderSection(w io.Writer, s Section) error {
	if _, err := fmt.Fprintln(w, s.Heading); err != nil {
		return err
	}
	for _, f := range s.Fields {
		if _, err := fmt.Fprintf(w, "  %s: %s\n", f.Label, f.Value); err != nil {
			return err
		}
	}
	for _, d := range s.Details {
		if _, err := fmt.Fprintf(w, "  %s\n", d); err != nil {
			return err
		}
	}
	return nil
}
