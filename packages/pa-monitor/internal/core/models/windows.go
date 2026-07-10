package models

import (
	"strconv"
	"strings"
)

// windows maps a Claude model id to its context-window size in tokens. Values
// are the authoritative per-model windows from the Claude model catalog: current
// frontier models (Opus 4.6/4.7/4.8, Sonnet 5/4.6, Fable 5) ship a 1M window as
// standard; the 4.5 family defaults to 200k (1M only via the [1m] beta suffix,
// handled in Window); Haiku-tier is 200k.
//
// Keep this in sync with new model launches. A model absent from this map is
// sized by the family heuristic in Window, which errs toward 1M for frontier
// families so a new release doesn't silently regress to the 200k default.
var windows = map[string]int{
	"claude-opus-4-8":           1_000_000,
	"claude-opus-4-7":           1_000_000,
	"claude-opus-4-6":           1_000_000,
	"claude-opus-4-5":           200_000,
	"claude-sonnet-5":           1_000_000,
	"claude-sonnet-4-6":         1_000_000,
	"claude-sonnet-4-5":         200_000,
	"claude-fable-5":            1_000_000,
	"claude-haiku-4-5":          200_000,
	"claude-haiku-4-5-20251001": 200_000,
}

// familyWindows sizes an unknown model by its family prefix, so a new model
// launch doesn't regress to the 200k default before this file is updated.
var familyWindows = []struct {
	prefix string
	window int
}{
	{"claude-haiku-", 200_000},
	{"claude-opus-", 1_000_000},
	{"claude-sonnet-", 1_000_000},
	{"claude-fable-", 1_000_000},
	{"claude-mythos-", 1_000_000},
}

const defaultWindow = 200_000

// Window returns (window, known) for a model id.
//
// Resolution order:
//  1. A trailing [<n>k|m] suffix (e.g. "claude-opus-4-8[1m]") is Claude Code's
//     explicit context-beta marker and wins as an authoritative window
//     declaration, regardless of the base model.
//  2. An exact match in the per-model window map.
//  3. A family-prefix heuristic (opus/sonnet/fable/mythos → 1M, haiku → 200k),
//     reported as known=false since it is a forward-looking guess.
//  4. The conservative 200k default (known=false) — fail loud: over-reporting
//     context % on a large-window model is more visible than silently
//     under-reporting on a small one.
func Window(model string) (int, bool) {
	base := model
	if open := strings.IndexByte(model, '['); open >= 0 && strings.HasSuffix(model, "]") {
		if w, ok := parseWindowSuffix(model[open+1 : len(model)-1]); ok {
			return w, true
		}
		base = model[:open]
	}
	if w, ok := windows[base]; ok {
		return w, true
	}
	for _, f := range familyWindows {
		if strings.HasPrefix(base, f.prefix) {
			return f.window, false
		}
	}
	return defaultWindow, false
}

// parseWindowSuffix interprets a bracketed size hint like "1m" or "200k" as a
// token count (1m → 1_000_000, 200k → 200_000). Returns ok=false unless the hint
// is a positive integer optionally suffixed with k or m.
func parseWindowSuffix(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	mult := 1
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1_000, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1_000_000, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}
