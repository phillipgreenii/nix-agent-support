package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

func TestRenderList_hidesOldColdTerminal_keepsLiveAndYoung(t *testing.T) {
	now := time.Unix(10_000, 0)
	doneTTL := time.Hour

	rows := []store.Session{
		{Name: "live-working", State: store.Working, TmuxSession: "cc-live-working", LastActivityAt: 1},
		{Name: "old-done-cold", State: store.Done, TmuxSession: "cc-old-done-cold", LastActivityAt: now.Add(-2 * time.Hour).Unix()},
		{Name: "young-done-cold", State: store.Done, TmuxSession: "cc-young-done-cold", LastActivityAt: now.Add(-10 * time.Minute).Unix()},
		{Name: "old-done-live", State: store.Done, TmuxSession: "cc-old-done-live", LastActivityAt: now.Add(-3 * time.Hour).Unix()},
	}
	live := map[string]bool{"cc-live-working": true, "cc-old-done-live": true}
	liveFn := func(_, target string) bool { return live[target] }

	out := renderList(rows, false, "", liveFn, "ccpool", now, doneTTL, 24*time.Hour)

	if !strings.Contains(out, "live-working") {
		t.Error("live working session was hidden")
	}
	if strings.Contains(out, "old-done-cold") {
		t.Error("old cold done session should be hidden in default view")
	}
	if !strings.Contains(out, "young-done-cold") {
		t.Error("young cold done session should be visible")
	}
	if !strings.Contains(out, "old-done-live") {
		t.Error("a still-live done session must never be hidden")
	}
}

func TestRenderList_allShowsEverything(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "old-done-cold", State: store.Done, TmuxSession: "cc-x", LastActivityAt: now.Add(-5 * time.Hour).Unix()},
	}
	liveFn := func(_, _ string) bool { return false }
	out := renderList(rows, true /*all*/, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if !strings.Contains(out, "old-done-cold") {
		t.Error("--all must show cold terminal rows")
	}
}

func TestRenderList_stateFilter(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "a", State: store.Working, TmuxSession: "cc-a", LastActivityAt: now.Unix()},
		{Name: "b", State: store.NeedsInput, TmuxSession: "cc-b", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	out := renderList(rows, false, "needs_input", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if strings.Contains(out, "\na ") || strings.Contains(out, " a ") {
		t.Error("state filter should exclude 'a'")
	}
	if !strings.Contains(out, "b") {
		t.Error("state filter should include 'b'")
	}
	_ = context.Background()
}
