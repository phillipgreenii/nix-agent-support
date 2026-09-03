package render

// Glyphs is the single-glyph table for pr-pool's own health grammar:
// ok / cooling N s / failing ×N / paused / disabled / excluded / stale.
//
// Actual USE of these glyphs in a rendered pane or banner string is out of
// scope for this package — see the sibling packets covering Tasks 4.6-4.8.
// This package only defines the table; LegendModal (modal.go) is the one
// place here that renders it, for the operator's own legend popup.
type Glyphs struct {
	OK       string
	Cooling  string
	Failing  string
	Paused   string
	Disabled string
	Excluded string
	Stale    string
}

// DefaultGlyphs is pr-pool's health-grammar symbol table.
var DefaultGlyphs = Glyphs{
	OK:       "●",
	Cooling:  "◷",
	Failing:  "✗",
	Paused:   "⏸",
	Disabled: "○",
	Excluded: "⊘",
	Stale:    "☾",
}
