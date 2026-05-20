package tui

import (
	"strings"
	"testing"
)

// fixedZone produces a zoneSpec carrying `n` lines of static content.
func fixedZone(name string, n int, dropOrder int) zoneSpec {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = name + "-line-" + string(rune('A'+i))
	}
	return zoneSpec{name: name, content: strings.Join(lines, "\n"), dropOrder: dropOrder}
}

func bodyZone(label string) zoneSpec {
	return zoneSpec{
		name: "body",
		fill: true,
		renderFill: func(h int) string {
			lines := make([]string, h)
			for i := range lines {
				lines[i] = label + "-row"
			}
			return strings.Join(lines, "\n")
		},
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func TestLayoutZonesReturnsExactHeight(t *testing.T) {
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		bodyZone("body"),
		fixedZone("status", 1, 2),
	}
	for _, h := range []int{4, 5, 10, 30, 100} {
		out := layoutZones(zones, 80, h)
		if got := countLines(out); got != h {
			t.Errorf("height=%d: got %d lines, want %d\n%s", h, got, h, out)
		}
	}
}

func TestLayoutZonesDropPriority(t *testing.T) {
	// header has dropOrder=1 (drops first), status dropOrder=2 (drops second).
	// At h=5: header(3) + status(1) + body fits with body=1.
	// At h=4: header(3) + status(1) = 4, body would be 0 → header drops first
	//         (smallest dropOrder), leaving status(1) + body(3) = 4.
	// At h=2: only status(1) + body(1) survives.
	// At h=1: both header and status drop, body gets 1.
	zones := []zoneSpec{
		fixedZone("header", 3, 1), // drops first
		bodyZone("body"),
		fixedZone("status", 1, 2), // drops second
	}
	cases := []struct {
		height      int
		wantHeader  bool
		wantStatus  bool
		wantBodyMin int
	}{
		{height: 5, wantHeader: true, wantStatus: true, wantBodyMin: 1},
		{height: 4, wantHeader: false, wantStatus: true, wantBodyMin: 3}, // header drops, status stays, body=3
		{height: 3, wantHeader: false, wantStatus: true, wantBodyMin: 2}, // header drops, status stays, body=2
		{height: 2, wantHeader: false, wantStatus: true, wantBodyMin: 1}, // header dropped, body=1
		{height: 1, wantHeader: false, wantStatus: false, wantBodyMin: 1}, // both dropped
	}
	for _, c := range cases {
		out := layoutZones(zones, 80, c.height)
		if got := strings.Contains(out, "header-line-A"); got != c.wantHeader {
			t.Errorf("h=%d header presence = %v, want %v\n%s", c.height, got, c.wantHeader, out)
		}
		if got := strings.Contains(out, "status-line-A"); got != c.wantStatus {
			t.Errorf("h=%d status presence = %v, want %v\n%s", c.height, got, c.wantStatus, out)
		}
		if got := strings.Count(out, "body-row"); got < c.wantBodyMin {
			t.Errorf("h=%d body rows = %d, want >= %d\n%s", c.height, got, c.wantBodyMin, out)
		}
	}
}

func TestLayoutZonesPadsShortContent(t *testing.T) {
	// Header is 3 lines, status 1 line, body's renderFill returns only 1 row
	// regardless of allocated height. With height=8, body is allocated 4 rows
	// but emits only 1 — total raw content = 3 + 1 + 1 = 5. padOrTruncate
	// must pad the trailing 3 rows with blank lines.
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		{
			name: "body", fill: true,
			renderFill: func(_ int) string { return "body-row" },
		},
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 8)
	if got := countLines(out); got != 8 {
		t.Errorf("lines = %d, want 8\n%s", got, out)
	}
	// Last 3 lines should be empty.
	lines := strings.Split(out, "\n")
	for i := 5; i < 8; i++ {
		if lines[i] != "" {
			t.Errorf("expected blank line at %d, got %q", i, lines[i])
		}
	}
}

func TestLayoutZonesTruncatesOverlongContent(t *testing.T) {
	// Body renderFill produces 10 rows but only 4 fit (height=8 - header 3 - status 1).
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		{
			name: "body", fill: true,
			renderFill: func(_ int) string {
				lines := make([]string, 10)
				for i := range lines {
					lines[i] = "body-row"
				}
				return strings.Join(lines, "\n")
			},
		},
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 8)
	if got := countLines(out); got != 8 {
		t.Errorf("lines = %d, want 8\n%s", got, out)
	}
}

func TestLayoutZonesHeightZeroBypass(t *testing.T) {
	zones := []zoneSpec{
		fixedZone("header", 2, 1),
		bodyZone("body"),
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 0)
	if !strings.Contains(out, "header-line-A") || !strings.Contains(out, "status-line-A") {
		t.Errorf("h=0 should emit zones unmodified for headless mode:\n%s", out)
	}
}
