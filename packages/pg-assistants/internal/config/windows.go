package config

import "strings"

// windows maps an exact Claude model id to its context window in tokens.
//
// Seeded from pa-monitor's `internal/core/models/windows.go`, WITHOUT its
// family fallback: that fallback sizes an unknown model by its family prefix,
// which over-sizes a model it does not know and so would accept a kill point
// the session can never reach. Here an id that is not in the table is unknown,
// and the config needs an explicit context.window for it.
//
// The current ids (opus-5-5, opus-5, sonnet-5-5, haiku-5-5, fable-5-2,
// fable-5-1) were checked on 2026-11-17 against the Claude docs' models
// overview (the context-window column); the Models API `max_input_tokens`
// could not be queried from the host that day (no `ant` CLI or API
// credential). The legacy 4.5 rows stay at 200k: their 1M window needs the
// `[1m]` suffix. Refresh with the procedure in pa-monitor's
// `docs/runbooks/refresh-model-tables.md` (table 1), and keep the two tables
// in step.
var windows = map[string]int64{
	"claude-fable-5-2":          1_000_000,
	"claude-fable-5-1":          1_000_000,
	"claude-fable-5":            1_000_000,
	"claude-opus-5-5":           1_000_000,
	"claude-opus-5":             1_000_000,
	"claude-opus-4-8":           1_000_000,
	"claude-opus-4-7":           1_000_000,
	"claude-opus-4-6":           1_000_000,
	"claude-opus-4-5":           200_000,
	"claude-sonnet-5-5":         1_000_000,
	"claude-sonnet-5":           1_000_000,
	"claude-sonnet-4-6":         1_000_000,
	"claude-sonnet-4-5":         200_000,
	"claude-haiku-5-5":          1_000_000,
	"claude-haiku-4-5":          200_000,
	"claude-haiku-4-5-20251001": 200_000,
}

// oneMillionSuffix is Claude Code's explicit 1M-context marker on a model id.
const oneMillionSuffix = "[1m]"

// ContextWindow returns the context window of an exact model id and whether
// the id is known. A `[1m]` suffix on a known id selects the 1M window; any
// other id, including an unknown id with the suffix, is unknown.
func ContextWindow(model string) (int64, bool) {
	base, oneMillion := strings.CutSuffix(model, oneMillionSuffix)
	w, ok := windows[base]
	if !ok {
		return 0, false
	}
	if oneMillion {
		return 1_000_000, true
	}
	return w, true
}
