package render

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func makeSessionView(status session.Status, le *transcript.ErrorRecord, retryable bool, nudge *aggregate.PendingNudge) *aggregate.SessionView {
	return &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "test-id",
			Status:    status,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError:          le,
			LastErrorRetryable: retryable,
			PendingNudge:       nudge,
		},
	}
}

// TestSessionGlyphWorking verifies that Working status always returns the
// working glyph regardless of LastError or PendingNudge.
func TestSessionGlyphWorking(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError,
		IsTerminal: true,
		At:         time.Now(),
	}
	nudge := &aggregate.PendingNudge{Sources: []string{"disrupted"}}
	sv := makeSessionView(session.Working, le, true, nudge)

	glyph := sessionGlyph(sv, Theme{})
	if strings.Contains(glyph, "⚠") || strings.Contains(glyph, "✗") {
		t.Errorf("Working session should not have error glyph; got %q", glyph)
	}
	if strings.Contains(glyph, "↪") {
		t.Errorf("Working session should not have nudge marker; got %q", glyph)
	}
}

// TestSessionGlyphIdleRetryableTerminalError verifies that an idle session with
// a terminal retryable error shows the ⚠ glyph.
func TestSessionGlyphIdleRetryableTerminalError(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError,
		IsTerminal: true,
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, true, nil)

	glyph := sessionGlyph(sv, Theme{})
	if !strings.Contains(glyph, "⚠") {
		t.Errorf("idle+retryable terminal error: expected ⚠ glyph; got %q", glyph)
	}
}

// TestSessionGlyphIdleNonRetryableTerminalError verifies that an idle session
// with a terminal non-retryable error shows the ✗ glyph.
func TestSessionGlyphIdleNonRetryableTerminalError(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrInvalidRequest,
		IsTerminal: true,
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, false, nil)

	glyph := sessionGlyph(sv, Theme{})
	if !strings.Contains(glyph, "✗") {
		t.Errorf("idle+non-retryable terminal error: expected ✗ glyph; got %q", glyph)
	}
}

// TestSessionGlyphIdleNonTerminalError verifies that a non-terminal error does
// not trigger an error glyph — the regular idle symbol is returned.
func TestSessionGlyphIdleNonTerminalError(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError,
		IsTerminal: false, // not terminal → no glyph change
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, true, nil)

	glyph := sessionGlyph(sv, Theme{})
	if strings.Contains(glyph, "⚠") || strings.Contains(glyph, "✗") {
		t.Errorf("non-terminal error should not produce error glyph; got %q", glyph)
	}
}

// TestSessionGlyphIdlePendingNudge verifies that a pending nudge appends ↪
// when the primary glyph is not Working.
func TestSessionGlyphIdlePendingNudge(t *testing.T) {
	nudge := &aggregate.PendingNudge{Sources: []string{"disrupted", "manual"}}
	sv := makeSessionView(session.Idle, nil, false, nudge)

	glyph := sessionGlyph(sv, Theme{})
	if !strings.Contains(glyph, "↪") {
		t.Errorf("idle session with pending nudge: expected ↪ marker; got %q", glyph)
	}
}

// TestSessionGlyphEscalatedIsNonRetryableGlyph verifies that an escalated
// error (retryable class but the verdict flipped to false) shows the ✗
// (non-retryable) glyph.
func TestSessionGlyphEscalatedIsNonRetryableGlyph(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError, // inherently retryable class
		IsTerminal: true,
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, false, nil) // verdict flipped → escalated

	glyph := sessionGlyph(sv, Theme{})
	if !strings.Contains(glyph, "✗") {
		t.Errorf("escalated error: expected ✗ glyph (LastErrorRetryable=false); got %q", glyph)
	}
	if strings.Contains(glyph, "⚠") {
		t.Errorf("escalated error: should not have ⚠ glyph; got %q", glyph)
	}
}
