package tui

import (
	"strings"
	"testing"
)

// TestLayoutZones_PinnedNeverDrops is this packet's own red-first test
// [design: Task 4.6 Step 1]: a zone list whose PINNED zones' own combined
// height exceeds the terminal height still renders both pinned zones in
// full, dropping every non-pinned, non-fill zone.
func TestLayoutZones_PinnedNeverDrops(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", content: "TOP-A\nTOP-B\nTOP-C\nTOP-D", pinned: true},
		{name: "attention", content: "ATTN", dropOrder: 1},
		{name: "poll-error", content: "POLLERR", dropOrder: 2},
		{name: "activity", content: "ACTIVITY", dropOrder: 3},
		{name: "listeners", fill: true, renderFill: func(h int) string { return "FILL" }},
		{name: "footer", content: "FOOTER-A\nFOOTER-B\nFOOTER-C", pinned: true},
	}
	// Pinned zones alone total 4+3 = 7 lines; height is only 6.
	got := layoutZones(zones, 40, 6)

	for _, want := range []string{"TOP-A", "TOP-B", "TOP-C", "TOP-D", "FOOTER-A", "FOOTER-B", "FOOTER-C"} {
		if !strings.Contains(got, want) {
			t.Errorf("layoutZones dropped pinned content %q; got:\n%s", want, got)
		}
	}
	for _, dropped := range []string{"ATTN", "POLLERR", "ACTIVITY"} {
		if strings.Contains(got, dropped) {
			t.Errorf("layoutZones kept non-pinned, non-fill zone %q it should have dropped; got:\n%s", dropped, got)
		}
	}
}

// TestHighestPriorityDroppable_NeverReturnsPinnedOrFill is the drop
// search's own acceptance bar: it never returns a pinned OR fill zone's
// index, even when every non-pinned/non-fill zone has already been
// removed.
func TestHighestPriorityDroppable_NeverReturnsPinnedOrFill(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", pinned: true},
		{name: "fill", fill: true},
		{name: "footer", pinned: true},
	}
	if idx := highestPriorityDroppable(zones); idx != -1 {
		t.Fatalf("highestPriorityDroppable = %d, want -1 (only pinned/fill zones present)", idx)
	}

	mixed := []zoneSpec{
		{name: "top", pinned: true},
		{name: "attention", dropOrder: 1},
		{name: "activity", dropOrder: 3},
		{name: "fill", fill: true},
		{name: "footer", pinned: true},
	}
	idx := highestPriorityDroppable(mixed)
	if idx < 0 || mixed[idx].pinned || mixed[idx].fill {
		t.Fatalf("highestPriorityDroppable returned index %d (%+v), want the smallest-dropOrder non-pinned/non-fill zone", idx, mixed[idx])
	}
	if mixed[idx].name != "attention" {
		t.Errorf("highestPriorityDroppable = %q, want %q (smallest dropOrder)", mixed[idx].name, "attention")
	}
}

// TestLayoutZones_BelowFloorOnlyPinnedRender pins the design's own literal
// floor: below terminal height 4, only the two pinned zones render --
// even the fill zone is dropped [design: Task 4.6 Interfaces].
func TestLayoutZones_BelowFloorOnlyPinnedRender(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", content: "TOP", pinned: true},
		{name: "activity", content: "ACTIVITY", dropOrder: 3},
		{name: "fill", fill: true, renderFill: func(h int) string { return "FILL-CONTENT" }},
		{name: "footer", content: "FOOTER", pinned: true},
	}
	for _, h := range []int{1, 2, 3} {
		got := layoutZones(zones, 40, h)
		if !strings.Contains(got, "TOP") {
			t.Errorf("height=%d: pinned zone TOP missing; got %q", h, got)
		}
		if strings.Contains(got, "FILL-CONTENT") {
			t.Errorf("height=%d: fill zone rendered below the pinnedFloorHeight; got %q", h, got)
		}
		if strings.Contains(got, "ACTIVITY") {
			t.Errorf("height=%d: non-pinned zone rendered below the pinnedFloorHeight; got %q", h, got)
		}
	}
}

// TestLayoutZones_NormalCaseExactHeight is the ordinary (non-pathological)
// case: with enough height for everything, layoutZones returns exactly
// `height` lines and drops nothing.
func TestLayoutZones_NormalCaseExactHeight(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", content: "TOP", pinned: true},
		{name: "attention", content: "ATTN", dropOrder: 1},
		{name: "fill", fill: true, renderFill: func(h int) string {
			lines := make([]string, h)
			for i := range lines {
				lines[i] = "ROW"
			}
			return strings.Join(lines, "\n")
		}},
		{name: "footer", content: "FOOTER", pinned: true},
	}
	got := layoutZones(zones, 40, 10)
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != 10 {
		t.Fatalf("layoutZones returned %d lines, want exactly 10; got:\n%s", len(gotLines), got)
	}
	for _, want := range []string{"TOP", "ATTN", "FOOTER"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing zone %q; got:\n%s", want, got)
		}
	}
}

// TestLayoutZones_DropsLowestPriorityFirst exercises the drop order itself
// under moderate height pressure: with just enough height pressure to
// force ONE drop, the smallest-dropOrder zone (attention) goes, not a
// higher-priority one (activity).
func TestLayoutZones_DropsLowestPriorityFirst(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", content: "TOP", pinned: true},
		{name: "attention", content: "ATTN", dropOrder: 1},
		{name: "activity", content: "ACTIVITY", dropOrder: 3},
		{name: "fill", fill: true, renderFill: func(h int) string { return "FILL" }},
		{name: "footer", content: "FOOTER", pinned: true},
	}
	// top(1) + attn(1) + activity(1) + footer(1) = 4 lines used; height=4
	// leaves 0 for fill -- forces exactly one drop (attention, the
	// smallest dropOrder) so fill gets >=1 row.
	got := layoutZones(zones, 40, 4)
	if strings.Contains(got, "ATTN") {
		t.Errorf("expected the lowest-priority zone (attention) to drop first; got:\n%s", got)
	}
	if !strings.Contains(got, "ACTIVITY") {
		t.Errorf("expected activity (higher priority than attention) to survive; got:\n%s", got)
	}
}

// TestLayoutZones_HeadlessModeConcatenatesWithNoDropping (height == 0) is
// the test/headless escape hatch: every zone renders in source order, with
// no dropping or padding.
func TestLayoutZones_HeadlessModeConcatenatesWithNoDropping(t *testing.T) {
	zones := []zoneSpec{
		{name: "top", content: "TOP", pinned: true},
		{name: "attention", content: "ATTN", dropOrder: 1},
		{name: "footer", content: "FOOTER", pinned: true},
	}
	got := layoutZones(zones, 40, 0)
	for _, want := range []string{"TOP", "ATTN", "FOOTER"} {
		if !strings.Contains(got, want) {
			t.Errorf("headless mode dropped %q; got %q", want, got)
		}
	}
}
