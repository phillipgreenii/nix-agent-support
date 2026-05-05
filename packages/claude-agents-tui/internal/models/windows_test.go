package models

import "testing"

func TestWindowKnown(t *testing.T) {
	cases := map[string]int{
		"claude-opus-4-7":           1_000_000,
		"claude-sonnet-4-6":         200_000,
		"claude-haiku-4-5-20251001": 200_000,
	}
	for id, want := range cases {
		if got, known := Window(id); !known || got != want {
			t.Errorf("Window(%q) = (%d, %v), want (%d, true)", id, got, known, want)
		}
	}
}

func TestWindowUnknownFallback(t *testing.T) {
	got, known := Window("future-model-999")
	if known {
		t.Error("expected unknown")
	}
	if got != 200_000 {
		t.Errorf("fallback window = %d, want 200_000", got)
	}
}
