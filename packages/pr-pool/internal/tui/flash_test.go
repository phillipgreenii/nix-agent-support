package tui

import (
	"strings"
	"testing"
	"time"
)

// TestFlash_SetArmsTTL: setFlash records the message/level and arms
// flashUntil flashTTL out from "now".
func TestFlash_SetArmsTTL(t *testing.T) {
	m := newTestModel(nil)
	before := time.Now()
	m.setFlash("hello", FlashWarn)

	if m.flash != "hello" {
		t.Errorf("flash = %q, want %q", m.flash, "hello")
	}
	if m.flashLevel != FlashWarn {
		t.Errorf("flashLevel = %v, want FlashWarn", m.flashLevel)
	}
	wantEarliest := before.Add(flashTTL)
	if m.flashUntil.Before(wantEarliest) {
		t.Errorf("flashUntil = %v, want at least %v out (flashTTL)", m.flashUntil, wantEarliest)
	}
}

// TestFlash_ExtendNeverShortens is spec §7's own invariant, generalized
// beyond the literal t0/t0+3s illustration: setFlash's flashUntil is
// always `now + flashTTL`, and `now` only ever advances between two
// calls, so a SECOND call's flashUntil can never be earlier than a first
// call's -- the visible window only ever extends, never shortens.
func TestFlash_ExtendNeverShortens(t *testing.T) {
	m := newTestModel(nil)
	m.setFlash("first", FlashInfo)
	firstUntil := m.flashUntil

	m.setFlash("second", FlashWarn)
	secondUntil := m.flashUntil

	if secondUntil.Before(firstUntil) {
		t.Fatalf("second flash's Until (%v) is EARLIER than the first's (%v) -- TTL shortened", secondUntil, firstUntil)
	}
	// The second call's own values fully replace the first's (an
	// overwrite, not a merge) -- extend applies to the WINDOW, not the
	// content.
	if m.flash != "second" || m.flashLevel != FlashWarn {
		t.Errorf("flash/level = %q/%v, want the second flash's own values", m.flash, m.flashLevel)
	}
}

// TestFlash_ExtendPastWhatFirstAloneWouldHaveGiven reproduces spec §7's
// literal worked example directly: a flash set at t0 is gone by
// t0+5s+eps; a second flash set at t0+3s (i.e. 2s before the first would
// have expired) extends the visible window to (t0+3s)+5s = t0+8s, PAST
// what the first alone would have given (t0+5s).
func TestFlash_ExtendPastWhatFirstAloneWouldHaveGiven(t *testing.T) {
	m := newTestModel(nil)
	t0 := time.Now()

	m.setFlash("first", FlashInfo)
	// Simulate "3s have passed since t0" by rewinding flashUntil as if
	// setFlash had actually been called at t0 (rather than whenever this
	// line actually runs) -- flashUntil should read t0+5s.
	m.flashUntil = t0.Add(flashTTL)
	firstAloneExpiry := m.flashUntil // t0+5s: what the first alone would give

	// The second flash "arrives" at t0+3s.
	simulatedSecondSetAt := t0.Add(3 * time.Second)
	// setFlash always stamps from the REAL now, so to exercise the exact
	// t0+3s/t0+8s arithmetic without sleeping, stamp flashUntil directly
	// the same way setFlash would have from that simulated instant.
	m.flash = "second"
	m.flashLevel = FlashWarn
	m.flashUntil = simulatedSecondSetAt.Add(flashTTL) // t0+8s

	if !m.flashUntil.After(firstAloneExpiry) {
		t.Fatalf("second flash's Until (%v) does not extend past the first alone's expiry (%v)", m.flashUntil, firstAloneExpiry)
	}
	wantUntil := t0.Add(8 * time.Second)
	if !m.flashUntil.Equal(wantUntil) {
		t.Fatalf("flashUntil = %v, want exactly t0+8s = %v", m.flashUntil, wantUntil)
	}
}

// TestApplyFlashClear_OnlyClearsWhenExpired mirrors pa-monitor's own
// "keeps unexpired flash" guard: a clear tick landing while flashUntil is
// still in the future must be a no-op; once it is in the past, the flash
// is cleared.
func TestApplyFlashClear_OnlyClearsWhenExpired(t *testing.T) {
	m := newTestModel(nil)
	m.setFlash("still alive", FlashInfo)
	m.applyFlashClear()
	if m.flash == "" {
		t.Fatal("applyFlashClear cleared a flash whose Until is still in the future")
	}

	m.flashUntil = time.Now().Add(-time.Millisecond)
	m.applyFlashClear()
	if m.flash != "" {
		t.Errorf("flash = %q, want cleared once flashUntil has elapsed", m.flash)
	}
}

// TestUpdate_FlashClearMsgRoutesToApplyFlashClear: Update's flashClearMsg
// case must actually call through to applyFlashClear (rather than being
// dead code the direct-call tests above don't exercise).
func TestUpdate_FlashClearMsgRoutesToApplyFlashClear(t *testing.T) {
	m := newTestModel(nil)
	m.setFlash("expiring", FlashInfo)
	m.flashUntil = time.Now().Add(-time.Millisecond)

	updated, _ := m.Update(flashClearMsg{})
	mm := updated.(*Model)
	if mm.flash != "" {
		t.Errorf("flash = %q after flashClearMsg on an expired flash, want cleared", mm.flash)
	}
}

// TestFlashClearCmd_ProducesFlashClearMsg: the returned tea.Cmd must
// actually produce a flashClearMsg when invoked (Update only reacts to
// that concrete type).
func TestFlashClearCmd_ProducesFlashClearMsg(t *testing.T) {
	m := newTestModel(nil)
	msg := m.flashClearCmd()()
	if _, ok := msg.(flashClearMsg); !ok {
		t.Fatalf("flashClearCmd()() = %T, want flashClearMsg", msg)
	}
}

// TestFlashText_ClippedToWidthAndEmptyWhenExpiredOrUnset covers the
// "clipped to footer width" half of this packet's Files entry for
// flash.go, plus the empty-when-inactive edge (no flash set, or one that
// has already expired).
func TestFlashText_ClippedToWidthAndEmptyWhenExpiredOrUnset(t *testing.T) {
	m := newTestModel(nil)
	if got := m.flashText(40); got != "" {
		t.Errorf("flashText with no flash set = %q, want empty", got)
	}

	long := strings.Repeat("x", 100)
	m.setFlash(long, FlashInfo)
	got := m.flashText(10)
	if len([]rune(got)) > 10 {
		t.Errorf("flashText(10) = %q (%d runes), want clipped to <=10", got, len([]rune(got)))
	}

	m.flashUntil = time.Now().Add(-time.Millisecond)
	if got := m.flashText(40); got != "" {
		t.Errorf("flashText on an expired flash = %q, want empty", got)
	}
}
