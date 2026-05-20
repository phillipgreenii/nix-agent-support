package models

var windows = map[string]int{
	"claude-opus-4-7":           1_000_000,
	"claude-opus-4-6":           200_000,
	"claude-opus-4-5":           200_000,
	"claude-sonnet-4-6":         200_000,
	"claude-sonnet-4-5":         200_000,
	"claude-haiku-4-5-20251001": 200_000,
}

// Window returns (window, known). Unknown models fall back to 200_000.
func Window(model string) (int, bool) {
	if w, ok := windows[model]; ok {
		return w, true
	}
	return 200_000, false
}
