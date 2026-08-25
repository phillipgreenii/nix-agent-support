package sectionrender

import (
	"errors"
	"strings"
	"testing"
)

// TestRender_SinglePopulatedSection pins the header/indented-detail shape
// (matching cmd/pg-pr/sync_duplicates.go's renderDuplicateReports) with a
// short golden string literal.
func TestRender_SinglePopulatedSection(t *testing.T) {
	sections := []Section{
		{
			Heading: "workspace demo",
			Fields: []Field{
				{Label: "scanned", Value: "12 open PRs"},
			},
			Details: []string{
				"ok no duplicated bead identities",
			},
		},
	}

	var buf strings.Builder
	if err := Render(&buf, sections...); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "workspace demo\n" +
		"  scanned: 12 open PRs\n" +
		"  ok no duplicated bead identities\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRender_UnknownMarkerField proves the exported Unknown marker renders
// verbatim, and is distinguishable from an empty value.
func TestRender_UnknownMarkerField(t *testing.T) {
	section := Section{
		Heading: "pr 42",
		Fields: []Field{
			{Label: "reviewer", Value: Unknown},
		},
	}

	var buf strings.Builder
	if err := Render(&buf, section); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "pr 42\n  reviewer: -\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Distinguishable from an empty value: an empty Value renders
	// differently from the Unknown marker.
	var emptyBuf strings.Builder
	emptySection := Section{Heading: "pr 42", Fields: []Field{{Label: "reviewer", Value: ""}}}
	if err := Render(&emptyBuf, emptySection); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}
	if buf.String() == emptyBuf.String() {
		t.Errorf("Unknown marker output must differ from an empty value's output, both rendered as %q", buf.String())
	}

	// OrUnknown is the helper form of the same marker.
	if got := OrUnknown(""); got != Unknown {
		t.Errorf("OrUnknown(\"\") = %q, want %q", got, Unknown)
	}
	if got := OrUnknown("assigned"); got != "assigned" {
		t.Errorf("OrUnknown(%q) = %q, want the original value unchanged", "assigned", got)
	}
}

// TestRender_ZeroFieldSection proves a Section with no Fields and no
// Details still renders its Heading line rather than disappearing. A
// sibling caller's "no section silently dropped" acceptance criterion
// depends on this structurally.
func TestRender_ZeroFieldSection(t *testing.T) {
	section := Section{Heading: "empty section"}

	var buf strings.Builder
	if err := Render(&buf, section); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "empty section\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRender_MultipleSections proves multiple sections render in call
// order, each with its own heading, separated by the exact pinned
// Separator (a single blank line) between (not before the first, not
// after the last) section.
func TestRender_MultipleSections(t *testing.T) {
	sections := []Section{
		{Heading: "first", Fields: []Field{{Label: "a", Value: "1"}}},
		{Heading: "second", Fields: []Field{{Label: "b", Value: "2"}}},
	}

	var buf strings.Builder
	if err := Render(&buf, sections...); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	want := "first\n  a: 1\n" + Separator + "second\n  b: 2\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Pin the exact separator text itself: a single blank line, i.e. one
	// literal "\n" written between the two sections' rendered blocks.
	if Separator != "\n" {
		t.Errorf("Separator = %q, want %q", Separator, "\n")
	}
}

// errWriter is a fake io.Writer that always fails, for
// TestRender_WriteErrorPropagates.
type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

// TestRender_WriteErrorPropagates proves Render returns a Write error to
// the caller rather than panicking or silently swallowing it. This is a
// deliberate departure from cmd/pg-pr/sync_duplicates.go's
// renderDuplicateReports, which swallows write errors as "non-fatal — the
// caller may have closed the writer"; that judgment call is left to each
// caller here, not made unilaterally by this package.
func TestRender_WriteErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: writer closed")
	w := errWriter{err: wantErr}

	err := Render(w, Section{Heading: "anything", Fields: []Field{{Label: "x", Value: "y"}}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
}

// flakyWriter fails on exactly the failAt'th Write call (0-indexed),
// succeeding on every other call and appending what it wrote to buf. Unlike
// errWriter (which fails EVERY call, so any error path exercised after the
// first failure returns the SAME error value and a test asserting only
// errors.Is cannot tell "returned immediately" from "kept going and failed
// again later"), flakyWriter lets a test pin exactly WHICH write call
// inside Render/renderSection triggers the returned error and confirm
// nothing after it was written — i.e. that the failing branch actually
// returns rather than falling through.
type flakyWriter struct {
	buf    strings.Builder
	failAt int
	err    error
	calls  int
}

func (w *flakyWriter) Write(p []byte) (int, error) {
	call := w.calls
	w.calls++
	if call == w.failAt {
		return 0, w.err
	}
	return w.buf.Write(p)
}

// TestRender_HeadingWriteErrorStopsRenderingImmediately pins that a failure
// on the very first write (a Section's heading line) returns immediately —
// nothing from that section's Fields or Details is written afterward.
// errWriter's always-fail behavior cannot distinguish this from "kept
// rendering and failed again on the next write," because both paths
// surface the identical error value; flakyWriter's single, isolated
// failure can.
func TestRender_HeadingWriteErrorStopsRenderingImmediately(t *testing.T) {
	wantErr := errors.New("boom: heading write failed")
	w := &flakyWriter{failAt: 0, err: wantErr}

	err := Render(w, Section{
		Heading: "h",
		Fields:  []Field{{Label: "a", Value: "b"}},
		Details: []string{"d"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
	if w.buf.String() != "" {
		t.Errorf("buf = %q, want empty — nothing should be written after the heading write fails", w.buf.String())
	}
}

// TestRender_FieldWriteErrorStopsRenderingImmediately pins the same
// immediate-return property for a failure partway through a Section's
// Fields: the failing field's write is the last thing attempted — no
// later field and no Detail line is written.
func TestRender_FieldWriteErrorStopsRenderingImmediately(t *testing.T) {
	wantErr := errors.New("boom: field write failed")
	w := &flakyWriter{failAt: 1, err: wantErr} // call 0 = heading (succeeds), call 1 = first field (fails)

	err := Render(w, Section{
		Heading: "h",
		Fields:  []Field{{Label: "a", Value: "b"}, {Label: "c", Value: "d"}},
		Details: []string{"e"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
	if want := "h\n"; w.buf.String() != want {
		t.Errorf("buf = %q, want %q — only the heading should have been written before the first field fails", w.buf.String(), want)
	}
}

// TestRender_DetailWriteErrorStopsRenderingImmediately is the Details-loop
// counterpart of the Fields-loop test above.
func TestRender_DetailWriteErrorStopsRenderingImmediately(t *testing.T) {
	wantErr := errors.New("boom: detail write failed")
	w := &flakyWriter{failAt: 2, err: wantErr} // call 0 = heading, call 1 = field (both succeed), call 2 = first detail (fails)

	err := Render(w, Section{
		Heading: "h",
		Fields:  []Field{{Label: "a", Value: "b"}},
		Details: []string{"d1", "d2"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
	if want := "h\n  a: b\n"; w.buf.String() != want {
		t.Errorf("buf = %q, want %q — the second detail line must not be written once the first fails", w.buf.String(), want)
	}
}

// TestRender_SeparatorWriteErrorStopsRenderingImmediately pins the same
// property for the inter-section Separator write: on a >1-section Render,
// a failing separator write must abort BEFORE the next section's heading
// is attempted. No prior test in this file exercises a write failure with
// more than one section, so the Separator's own error path (Render's
// `if i > 0 { ... err != nil ... return err }`) was previously untested.
func TestRender_SeparatorWriteErrorStopsRenderingImmediately(t *testing.T) {
	wantErr := errors.New("boom: separator write failed")
	w := &flakyWriter{failAt: 1, err: wantErr} // call 0 = first section's heading (succeeds), call 1 = separator (fails)

	err := Render(w, Section{Heading: "first"}, Section{Heading: "second"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
	if want := "first\n"; w.buf.String() != want {
		t.Errorf("buf = %q, want %q — the second section must never be rendered once the separator write fails", w.buf.String(), want)
	}
}

// TestRender_MultipleDetailLines_AllWritten proves every Detail line is
// written when none of the writes fail — the success-path counterpart to
// the error tests above. Every other test in this file uses at most one
// Detail line, which cannot catch a mutation that returns after the FIRST
// Detail line unconditionally (Render would still "succeed," just having
// silently dropped every Detail line after the first).
func TestRender_MultipleDetailLines_AllWritten(t *testing.T) {
	var buf strings.Builder
	err := Render(&buf, Section{Heading: "h", Details: []string{"d1", "d2", "d3"}})
	if err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "h\n  d1\n  d2\n  d3\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}
