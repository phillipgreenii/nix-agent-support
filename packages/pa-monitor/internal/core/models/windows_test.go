package models

import "testing"

// TestWindowExactKnown pins the authoritative per-model context windows.
// Source: the Claude model catalog (claude-api skill, models.md) — all current
// frontier models (Opus 4.6/4.7/4.8, Sonnet 5/4.6, Fable 5) ship a 1M context
// window as standard; only Haiku-tier is 200k. Legacy 4.5-family default to
// 200k (1M available only via the [1m] beta suffix, covered separately).
func TestWindowExactKnown(t *testing.T) {
	cases := map[string]int{
		// Current frontier models — all 1M standard.
		"claude-opus-4-8":   1_000_000,
		"claude-opus-4-7":   1_000_000,
		"claude-opus-4-6":   1_000_000,
		"claude-sonnet-5":   1_000_000,
		"claude-sonnet-4-6": 1_000_000,
		"claude-fable-5":    1_000_000,
		// Haiku-tier — 200k.
		"claude-haiku-4-5":          200_000,
		"claude-haiku-4-5-20251001": 200_000,
		// Legacy 4.5 family — 200k without the 1m beta.
		"claude-opus-4-5":   200_000,
		"claude-sonnet-4-5": 200_000,
	}
	for id, want := range cases {
		if got, known := Window(id); !known || got != want {
			t.Errorf("Window(%q) = (%d, %v), want (%d, true)", id, got, known, want)
		}
	}
}

// TestWindowSuffixOverride: Claude Code appends a [1m] suffix to the model id
// when the 1M-context beta is active (e.g. "claude-opus-4-8[1m]"). That suffix
// is an explicit, authoritative window declaration and must win over the base
// model's default window.
func TestWindowSuffixOverride(t *testing.T) {
	cases := map[string]int{
		"claude-opus-4-8[1m]":    1_000_000,
		"claude-sonnet-4-5[1m]":  1_000_000, // base is 200k; the suffix bumps it
		"claude-opus-4-5[1m]":    1_000_000,
		"mystery-model[1m]":      1_000_000, // unknown base, explicit window
		"claude-haiku-4-5[200k]": 200_000,
	}
	for id, want := range cases {
		if got, known := Window(id); !known || got != want {
			t.Errorf("Window(%q) = (%d, %v), want (%d, true)", id, got, known, want)
		}
	}
}

// TestWindowFamilyFallback: a future model absent from the map should still be
// sized by its family rather than blindly defaulting to 200k. Frontier families
// (opus/sonnet/fable/mythos) now default to 1M; only haiku-tier is small. These
// are heuristic guesses, so known is false.
func TestWindowFamilyFallback(t *testing.T) {
	cases := map[string]int{
		"claude-opus-5":     1_000_000,
		"claude-sonnet-9":   1_000_000,
		"claude-fable-6":    1_000_000,
		"claude-mythos-5":   1_000_000,
		"claude-haiku-5":    200_000,
		"claude-haiku-9-99": 200_000,
	}
	for id, want := range cases {
		got, known := Window(id)
		if got != want {
			t.Errorf("Window(%q) = (%d, %v), want window %d", id, got, known, want)
		}
		if known {
			t.Errorf("Window(%q): family heuristic must report known=false", id)
		}
	}
}

// TestWindowUnknownFallback: a model matching no exact entry and no known family
// falls back to the conservative 200k default (fail-loud: better to over-report
// context % on a large-window model than silently under-report on a small one).
func TestWindowUnknownFallback(t *testing.T) {
	for _, id := range []string{"future-model-999", "<synthetic>", ""} {
		got, known := Window(id)
		if known {
			t.Errorf("Window(%q): expected unknown", id)
		}
		if got != 200_000 {
			t.Errorf("Window(%q) fallback = %d, want 200_000", id, got)
		}
	}
}
