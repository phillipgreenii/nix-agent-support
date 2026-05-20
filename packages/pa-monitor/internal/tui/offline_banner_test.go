package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOfflineBanner_EmptyWhenNoError(t *testing.T) {
	m := NewModel(Options{})
	if got := m.offlineBanner(time.Now()); got != "" {
		t.Errorf("no-error banner should be empty, got %q", got)
	}
}

func TestOfflineBanner_NeverConnectedMessage(t *testing.T) {
	m := NewModel(Options{})
	m.lastErr = errors.New("dial unix: no such file")
	got := m.offlineBanner(time.Now())
	if !strings.Contains(got, "unreachable") {
		t.Errorf("expected 'unreachable' in banner, got %q", got)
	}
}

func TestOfflineBanner_AgeMessageAfterFirstSuccess(t *testing.T) {
	m := NewModel(Options{})
	m.lastErr = errors.New("dial unix: no such file")
	m.lastSuccessAt = time.Now().Add(-12 * time.Second)
	got := m.offlineBanner(time.Now())
	if !strings.Contains(got, "offline") {
		t.Errorf("expected 'offline' in banner, got %q", got)
	}
	if !strings.Contains(got, "12s") {
		t.Errorf("expected '12s' age in banner, got %q", got)
	}
}
