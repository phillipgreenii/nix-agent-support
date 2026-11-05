package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

var windowNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func TestResolveWindowDefaultsToSevenDays(t *testing.T) {
	start := windowNow.Add(-7 * 24 * time.Hour)
	s, b, d, err := resolveWindow("search", time.Time{}, time.Time{}, windowNow)
	if err != nil || !s.Equal(start) || !d.Equal(start) || !b.IsZero() {
		t.Errorf("got (%v, %v, %v, %v), want since=default=%v and open before", s, b, d, err, start)
	}
}

func TestResolveWindowNarrowingAndBoundary(t *testing.T) {
	for name, since := range map[string]time.Time{
		"one hour":      windowNow.Add(-time.Hour),
		"exactly 7d":    windowNow.Add(-7 * 24 * time.Hour),
		"future":        windowNow.Add(time.Hour),
		"just inside 7": windowNow.Add(-7*24*time.Hour + time.Second),
	} {
		s, _, d, err := resolveWindow("sessions", since, time.Time{}, windowNow)
		if err != nil || !s.Equal(since) || !d.IsZero() {
			t.Errorf("%s: got (%v, %v, %v), want since unchanged, no default, no error", name, s, d, err)
		}
	}
}

func TestResolveWindowRejectsOlderThanSevenDays(t *testing.T) {
	for name, since := range map[string]time.Time{
		"one second past": windowNow.Add(-7*24*time.Hour - time.Second),
		"30 days":         windowNow.Add(-30 * 24 * time.Hour),
	} {
		_, _, _, err := resolveWindow("sessions", since, time.Time{}, windowNow)
		if err == nil {
			t.Fatalf("%s: want an error, got nil", name)
		}
		for _, want := range []string{"sessions:", "--since", "at most 7 days", "pg-claude-code-recorder search or query"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q lacks %q", name, err, want)
			}
		}
	}
}

func TestResolveWindowKeepsBefore(t *testing.T) {
	before := windowNow.Add(-24 * time.Hour)
	s, b, d, err := resolveWindow("search", time.Time{}, before, windowNow)
	if err != nil || !b.Equal(before) || !s.Equal(windowNow.Add(-7*24*time.Hour)) || d.IsZero() {
		t.Errorf("before only: got (%v, %v, %v, %v), want the default start and before unchanged", s, b, d, err)
	}
}

func TestSessionsAndSearchEnforceWindowAndDropAll(t *testing.T) {
	if _, _, _, _, err := parseSessionsArgs([]string{"--since", "8d"}, windowNow); err == nil || !strings.Contains(err.Error(), "at most 7 days") {
		t.Errorf("sessions --since 8d: got %v, want the 7-day error", err)
	}
	if _, _, _, _, err := parseSessionsArgs([]string{"--since", "7d"}, windowNow); err != nil {
		t.Errorf("sessions --since 7d: %v", err)
	}
	if _, _, _, _, err := parseSessionsArgs([]string{"--all"}, windowNow); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("sessions --all: got %v, want an unexpected-argument error naming --all", err)
	}
	_, _, since, _, _, err := parseSearchArgs([]string{"x", "--since", "2020-01-01T00:00:00Z"}, windowNow)
	if err == nil || !since.IsZero() || !strings.Contains(err.Error(), "search:") {
		t.Errorf("search old RFC3339 since: got (%v, %v), want the 7-day error", since, err)
	}
	_, _, since, _, _, err = parseSearchArgs([]string{"x"}, windowNow)
	if err != nil || !since.Equal(windowNow.Add(-7*24*time.Hour)) {
		t.Errorf("search default: got (%v, %v), want since 7 days ago", since, err)
	}
}

func TestWithNarrowHint(t *testing.T) {
	for _, e := range []error{client.ErrQueryTimeout, context.DeadlineExceeded} {
		got := withNarrowHint(e, "--since or --session")
		if !errors.Is(got, e) || !strings.Contains(got.Error(), "narrow it with --since or --session") || !strings.Contains(got.Error(), "pg-claude-code-recorder") {
			t.Errorf("withNarrowHint(%v) = %q", e, got)
		}
	}
	plain := errors.New("boom")
	if got := withNarrowHint(plain, "--since"); got != plain {
		t.Errorf("non-timeout error changed: %v", got)
	}
}
