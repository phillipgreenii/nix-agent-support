package main

import (
	"context"
	"encoding/json"
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

// TestRenderListJSON_fieldsAndLiveness pins the --json shape pr-pool's Runner.List
// unmarshals: a JSON array of objects with snake_case name/state/live/
// transcript_path/uuid/cwd. live is SEPARATE from state (tmux has-session
// liveness, derived from liveFn), not folded into state.
func TestRenderListJSON_fieldsAndLiveness(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "alpha", UUID: "uuid-123456789", State: store.Working, TmuxSession: "cc-alpha", TranscriptPath: "/t/alpha.jsonl", CWD: "/repo", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, target string) bool { return target == "cc-alpha" }
	out, err := renderListJSON(rows, false, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("renderListJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (%q)", len(got), out)
	}
	r := got[0]
	for k, want := range map[string]any{
		"name":            "alpha",
		"state":           "working",
		"live":            true,
		"transcript_path": "/t/alpha.jsonl",
		"uuid":            "uuid-123456789",
		"cwd":             "/repo",
	} {
		if r[k] != want {
			t.Errorf("%s = %#v, want %#v", k, r[k], want)
		}
	}
}

func TestRenderListJSON_allBypassesRetention(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "old-done", UUID: "u", State: store.Done, TmuxSession: "cc-x", CWD: "/repo", LastActivityAt: now.Add(-5 * time.Hour).Unix()},
	}
	liveFn := func(_, _ string) bool { return false }

	out, _ := renderListJSON(rows, false, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	var def []map[string]any
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(def) != 0 {
		t.Errorf("default JSON view should hide old cold done, got %q", out)
	}

	outAll, _ := renderListJSON(rows, true, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	var all []map[string]any
	if err := json.Unmarshal([]byte(outAll), &all); err != nil {
		t.Fatalf("unmarshal --all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("--all JSON must include old cold done, got %q", outAll)
	}
	if all[0]["live"] != false {
		t.Errorf("live = %#v, want false for a dead session", all[0]["live"])
	}
}

func TestRenderListJSON_transcriptPathEmptyStillPresentAndEmptyIsArray(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a", LastActivityAt: now.Unix()}, // TranscriptPath ""
	}
	liveFn := func(_, _ string) bool { return true }
	out, _ := renderListJSON(rows, false, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %q", out)
	}
	if v, ok := got[0]["transcript_path"]; !ok || v != "" {
		t.Errorf("transcript_path must be present even when empty: value=%#v present=%v", v, ok)
	}

	// No visible rows must marshal as [] (a JSON array), never null.
	emptyOut, _ := renderListJSON(nil, false, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if strings.TrimSpace(emptyOut) != "[]" {
		t.Errorf("empty list = %q, want []", emptyOut)
	}
}
