package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/gitfacet"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// strptr is a test helper for building *string facets.
func strptr(s string) *string { return &s }

// nilGit is a git resolver stub that always reports no git facets (cwd is not
// inside a work tree). Proves the null/absent branch hermetically.
func nilGit(string) gitfacet.Facets { return gitfacet.Facets{} }

// noPath is a path resolver stub that always errors, forcing the launch_dir
// fallback (mirrors a non-live session or a failed pane query).
func noPath(_, _ string) (string, error) { return "", errStub }

var errStub = stubErr("stub")

type stubErr string

func (e stubErr) Error() string { return string(e) }

func TestRenderList_hidesOldColdTerminal_keepsLiveAndYoung(t *testing.T) {
	now := time.Unix(10_000, 0)
	doneTTL := time.Hour

	rows := []store.Session{
		{Name: "live-working", State: store.Working, TmuxSession: "cc-live-working", LastActivityAt: 1},
		{Name: "old-done-cold", State: store.Idle, TmuxSession: "cc-old-done-cold", LastActivityAt: now.Add(-2 * time.Hour).Unix()},
		{Name: "young-done-cold", State: store.Idle, TmuxSession: "cc-young-done-cold", LastActivityAt: now.Add(-10 * time.Minute).Unix()},
		{Name: "old-done-live", State: store.Idle, TmuxSession: "cc-old-done-live", LastActivityAt: now.Add(-3 * time.Hour).Unix()},
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
		{Name: "old-done-cold", State: store.Idle, TmuxSession: "cc-x", LastActivityAt: now.Add(-5 * time.Hour).Unix()},
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

// TestRenderListJSON_fieldsAndLiveness pins the --json shape pg-router's Runner.List
// unmarshals: a JSON array of objects with snake_case name/state/live/
// transcript_path/uuid/launch_dir/cwd plus the git facets. live is SEPARATE
// from state (tmux has-session liveness, derived from liveFn), not folded into
// state. For a LIVE row, cwd is the live pane path (from pathFn) and the git
// facets come from the injected git resolver.
func TestRenderListJSON_fieldsAndLiveness(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "alpha", ClaudeSessionID: "uuid-123456789", State: store.Working, TmuxSession: "cc-alpha", TranscriptPath: "/t/alpha.jsonl", CWD: "/repo", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, target string) bool { return target == "cc-alpha" }
	// Live pane has wandered into a linked worktree under /repo.
	pathFn := func(_, target string) (string, error) {
		if target == "cc-alpha" {
			return "/repo/worktrees/wt1/sub", nil
		}
		return "", errStub
	}
	gitFn := func(cwd string) gitfacet.Facets {
		if cwd == "/repo/worktrees/wt1/sub" {
			return gitfacet.Facets{
				RepoRoot: strptr("/repo"),
				Worktree: strptr("/repo/worktrees/wt1"),
				Branch:   strptr("feature"),
			}
		}
		return gitfacet.Facets{}
	}
	out, err := renderListJSON(rows, false, "", liveFn, pathFn, gitFn, nil, "ccpool", now, time.Hour, 24*time.Hour)
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
		"name":              "alpha",
		"state":             "working",
		"live":              true,
		"transcript_path":   "/t/alpha.jsonl",
		"claude_session_id": "uuid-123456789",
		"launch_dir":        "/repo",
		"cwd":               "/repo/worktrees/wt1/sub", // LIVE pane path, not launch dir
		"git_repo_root":     "/repo",
		"worktree":          "/repo/worktrees/wt1",
		"branch":            "feature",
	} {
		if r[k] != want {
			t.Errorf("%s = %#v, want %#v", k, r[k], want)
		}
	}
}

// TestRenderListJSON_gitFacetsNullWhenNotInRepo proves the non-git branch: when
// the git resolver reports no facets, git_repo_root/worktree/branch are JSON
// null (or absent), while launch_dir/cwd remain present.
func TestRenderListJSON_gitFacetsNullWhenNotInRepo(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "alpha", ClaudeSessionID: "u", State: store.Working, TmuxSession: "cc-alpha", CWD: "/tmp/scratch", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	pathFn := func(_, _ string) (string, error) { return "/tmp/scratch", nil }
	out, err := renderListJSON(rows, false, "", liveFn, pathFn, nilGit, nil, "ccpool", now, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("renderListJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	r := got[0]
	if r["launch_dir"] != "/tmp/scratch" || r["cwd"] != "/tmp/scratch" {
		t.Errorf("launch_dir/cwd = %#v / %#v, want /tmp/scratch", r["launch_dir"], r["cwd"])
	}
	// git facets must be null or absent (with omitempty on *string nil, absent).
	for _, k := range []string{"git_repo_root", "worktree", "branch"} {
		if v, ok := r[k]; ok && v != nil {
			t.Errorf("%s = %#v, want null/absent outside a git repo", k, v)
		}
	}
}

// TestRenderListJSON_notLiveFallsBackToLaunchDir proves a non-live row reports
// cwd == launch_dir and carries no git facets (no pane to query, so the live
// path resolver is never consulted and the git resolver runs against launch_dir
// only when... it should NOT run for non-live rows: facets stay null).
func TestRenderListJSON_notLiveFallsBackToLaunchDir(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "cold", ClaudeSessionID: "u", State: store.Idle, TmuxSession: "cc-cold", CWD: "/launch/dir", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return false } // not live
	// pathFn/gitFn would report facets if called; assert they are NOT for a dead row.
	pathCalled, gitCalled := false, false
	pathFn := func(_, _ string) (string, error) { pathCalled = true; return "/should/not/be/used", nil }
	gitFn := func(string) gitfacet.Facets {
		gitCalled = true
		return gitfacet.Facets{RepoRoot: strptr("/x"), Worktree: strptr("/x"), Branch: strptr("b")}
	}
	out, _ := renderListJSON(rows, true, "", liveFn, pathFn, gitFn, nil, "ccpool", now, time.Hour, 24*time.Hour)
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	r := got[0]
	if r["cwd"] != "/launch/dir" || r["launch_dir"] != "/launch/dir" {
		t.Errorf("non-live cwd/launch_dir = %#v / %#v, want /launch/dir", r["cwd"], r["launch_dir"])
	}
	for _, k := range []string{"git_repo_root", "worktree", "branch"} {
		if v, ok := r[k]; ok && v != nil {
			t.Errorf("%s = %#v, want null/absent for a non-live row", k, v)
		}
	}
	if pathCalled {
		t.Error("path resolver must not be queried for a non-live row")
	}
	if gitCalled {
		t.Error("git resolver must not be queried for a non-live row")
	}
}

// TestRenderListJSON_liveButPathQueryFails proves cwd falls back to launch_dir
// when the live path query errors (e.g. session died between has-session and the
// pane query); git facets resolve against the fallback launch_dir.
func TestRenderListJSON_liveButPathQueryFails(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "alpha", ClaudeSessionID: "u", State: store.Working, TmuxSession: "cc-alpha", CWD: "/launch/dir", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	gitFn := func(cwd string) gitfacet.Facets {
		if cwd == "/launch/dir" {
			return gitfacet.Facets{RepoRoot: strptr("/launch/dir"), Worktree: strptr("/launch/dir"), Branch: strptr("main")}
		}
		return gitfacet.Facets{}
	}
	out, _ := renderListJSON(rows, false, "", liveFn, noPath, gitFn, nil, "ccpool", now, time.Hour, 24*time.Hour)
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	r := got[0]
	if r["cwd"] != "/launch/dir" {
		t.Errorf("cwd = %#v, want fallback /launch/dir when pane query fails", r["cwd"])
	}
	if r["branch"] != "main" {
		t.Errorf("branch = %#v, want main (resolved against fallback cwd)", r["branch"])
	}
}

func TestRenderListJSON_allBypassesRetention(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "old-done", ClaudeSessionID: "u", State: store.Idle, TmuxSession: "cc-x", CWD: "/repo", LastActivityAt: now.Add(-5 * time.Hour).Unix()},
	}
	liveFn := func(_, _ string) bool { return false }

	out, _ := renderListJSON(rows, false, "", liveFn, noPath, nilGit, nil, "ccpool", now, time.Hour, 24*time.Hour)
	var def []map[string]any
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(def) != 0 {
		t.Errorf("default JSON view should hide old cold done, got %q", out)
	}

	outAll, _ := renderListJSON(rows, true, "", liveFn, noPath, nilGit, nil, "ccpool", now, time.Hour, 24*time.Hour)
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
		{Name: "a", ClaudeSessionID: "u", State: store.Ready, TmuxSession: "cc-a", LastActivityAt: now.Unix()}, // TranscriptPath ""
	}
	liveFn := func(_, _ string) bool { return true }
	pathFn := func(_, _ string) (string, error) { return "/cwd", nil }
	out, _ := renderListJSON(rows, false, "", liveFn, pathFn, nilGit, nil, "ccpool", now, time.Hour, 24*time.Hour)
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
	emptyOut, _ := renderListJSON(nil, false, "", liveFn, pathFn, nilGit, nil, "ccpool", now, time.Hour, 24*time.Hour)
	if strings.TrimSpace(emptyOut) != "[]" {
		t.Errorf("empty list = %q, want []", emptyOut)
	}
}

func TestFilterRowsByExternalIDSet(t *testing.T) {
	rows := []store.Session{
		{ExternalID: "ext-a", State: store.Idle},
		{ExternalID: "ext-b", State: store.Idle},
		{ExternalID: "ext-c", State: store.Idle},
	}
	got := filterRowsByExternalIDSet(rows, map[string]bool{"ext-a": true, "ext-c": true})
	if len(got) != 2 || got[0].ExternalID != "ext-a" || got[1].ExternalID != "ext-c" {
		t.Fatalf("filtered = %+v, want ext-a,ext-c", got)
	}
}

func TestParseFilters_keyValuePairs(t *testing.T) {
	got, err := parseFilters([]string{"role=worker", "pool=pg-router"})
	if err != nil {
		t.Fatalf("parseFilters: %v", err)
	}
	want := map[string]string{"role": "worker", "pool": "pg-router"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFilters_rejectsMissingEquals(t *testing.T) {
	if _, err := parseFilters([]string{"role"}); err == nil {
		t.Fatal("filter without '=' must error")
	}
}

// TestRenderListPaths_statefilterPrintsOnlyMatchingLaunchDirs proves the
// pg2-lyriv --paths renderer: filtered to needs_input, it prints exactly the
// launch dirs (store.Session.CWD) of the matching rows, one per line, with no
// header, no state/name/live columns, and no other rows' paths leaking in.
func TestRenderListPaths_statefilterPrintsOnlyMatchingLaunchDirs(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{ExternalID: "a", State: store.Working, TmuxSession: "cc-a", CWD: "/wt/a", LastActivityAt: now.Unix()},
		{ExternalID: "b", State: store.NeedsInput, TmuxSession: "cc-b", CWD: "/wt/b", LastActivityAt: now.Unix()},
		{ExternalID: "c", State: store.NeedsInput, TmuxSession: "cc-c", CWD: "/wt/c", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	out := renderListPaths(rows, false, "needs_input", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	want := "/wt/b\n/wt/c\n"
	if out != want {
		t.Errorf("renderListPaths = %q, want %q", out, want)
	}
}

// TestRenderListPaths_usesLaunchDirEvenWhenGoneFromDisk proves --paths reports
// the STORED launch dir unconditionally, never a live pane-cwd query: the
// worktree directory pg2-lyriv is about can be deleted out from under a live
// needs_input session, so any renderer that depended on the directory still
// existing (a pane query, a git resolve) would be exactly as blind as the
// problem this flag exists to prevent. liveFn reports the row live (tmux
// itself does not notice a deleted cwd), yet the output is unaffected — no
// pane/git resolver is even wired into this renderer's signature.
func TestRenderListPaths_usesLaunchDirEvenWhenGoneFromDisk(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{ExternalID: "gone", State: store.NeedsInput, TmuxSession: "cc-gone", CWD: "/wt/deleted-from-disk", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true } // tmux still reports LIVE=yes
	out := renderListPaths(rows, false, "needs_input", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if out != "/wt/deleted-from-disk\n" {
		t.Errorf("renderListPaths = %q, want the stored launch dir regardless of disk state", out)
	}
}

// TestRenderListPaths_emptyWhenNoneVisible proves an empty/no-match result is
// an empty string, not a stray blank line — a `comm`/`grep -v` consumer must
// not see a spurious empty-path row.
func TestRenderListPaths_emptyWhenNoneVisible(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{ExternalID: "a", State: store.Working, TmuxSession: "cc-a", CWD: "/wt/a", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	out := renderListPaths(rows, false, "needs_input", liveFn, "ccpool", now, time.Hour, 24*time.Hour)
	if out != "" {
		t.Errorf("renderListPaths = %q, want empty when no row matches the state filter", out)
	}
}

func TestRenderListJSON_includesMetaObject(t *testing.T) {
	rows := []store.Session{{ExternalID: "ext-a", State: store.Idle, CWD: "/w"}}
	liveFn := func(_, _ string) bool { return false }
	metaFn := func(ext string) map[string]string { return map[string]string{"role": "worker"} }
	out, err := renderListJSON(rows, true, "", liveFn, nil, nil, metaFn, "ccpool",
		time.Unix(2000, 0), time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("renderListJSON: %v", err)
	}
	if !strings.Contains(out, `"meta":{"role":"worker"}`) {
		t.Errorf("meta not in JSON: %s", out)
	}
}

// TestRenderList_showsCloseReasonColumnWhenSet: one row with a reason, one
// without — the header must gain CLOSE_REASON, the first row's cell holds the
// reason, and the second row's cell is blank (ADR 0072, Decision 5).
func TestRenderList_showsCloseReasonColumnWhenSet(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{ExternalID: "evicted", State: store.Idle, TmuxSession: "cc-evicted", LastActivityAt: now.Unix(), CloseReason: "cap_eviction"},
		{ExternalID: "working", State: store.Working, TmuxSession: "cc-working", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, target string) bool { return target == "cc-working" }
	out := renderList(rows, false, "", liveFn, "ccpool", now, time.Hour, 24*time.Hour)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (header + 2 rows):\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "CLOSE_REASON") {
		t.Errorf("header missing CLOSE_REASON: %q", lines[0])
	}
	if !strings.Contains(lines[1], "cap_eviction") {
		t.Errorf("evicted row missing its close reason: %q", lines[1])
	}
	// The "working" row's fields, split on runs of whitespace: EXTERNAL_ID NAME
	// STATE LIVE LAST_ACTIVITY(2 fields) CLOSE_REASON CLAUDE_SESSION_ID. With an
	// empty CLOSE_REASON cell, "working" (the external_id) must be immediately
	// followed by the empty NAME field then STATE "working" then LIVE "yes" —
	// i.e. the row must NOT contain any cap_eviction/idle_ttl/operator/handler
	// token bleeding in from the blank cell.
	for _, reason := range []string{"cap_eviction", "idle_ttl", "operator", "handler"} {
		if strings.Contains(lines[2], reason) {
			t.Errorf("row with no close reason must have a blank cell, got %q", lines[2])
		}
	}
}

// TestRenderListJSON_includesCloseReason pins the "close_reason" JSON key
// (ADR 0072, Decision 4) — packet 5's cli.List parses this exact key.
func TestRenderListJSON_includesCloseReason(t *testing.T) {
	rows := []store.Session{
		{ExternalID: "evicted", State: store.Idle, CWD: "/w", CloseReason: "cap_eviction"},
		{ExternalID: "legacy", State: store.Idle, CWD: "/w"},
	}
	liveFn := func(_, _ string) bool { return false }
	out, err := renderListJSON(rows, true, "", liveFn, nil, nil, nil, "ccpool",
		time.Unix(2000, 0), time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("renderListJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (%q)", len(got), out)
	}
	if got[0]["close_reason"] != "cap_eviction" {
		t.Errorf(`row[0]["close_reason"] = %#v, want "cap_eviction"`, got[0]["close_reason"])
	}
	if got[1]["close_reason"] != "" {
		t.Errorf(`row[1]["close_reason"] = %#v, want "" (legacy row)`, got[1]["close_reason"])
	}
}
