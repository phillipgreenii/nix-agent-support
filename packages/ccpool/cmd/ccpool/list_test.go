package main

import (
	"context"
	"encoding/json"
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
// transcript_path/uuid/launch_dir/cwd plus the git facets. live is SEPARATE
// from state (tmux has-session liveness, derived from liveFn), not folded into
// state. For a LIVE row, cwd is the live pane path (from pathFn) and the git
// facets come from the injected git resolver.
func TestRenderListJSON_fieldsAndLiveness(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []store.Session{
		{Name: "alpha", UUID: "uuid-123456789", State: store.Working, TmuxSession: "cc-alpha", TranscriptPath: "/t/alpha.jsonl", CWD: "/repo", LastActivityAt: now.Unix()},
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
	out, err := renderListJSON(rows, false, "", liveFn, pathFn, gitFn, "ccpool", now, time.Hour, 24*time.Hour)
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
		"launch_dir":      "/repo",
		"cwd":             "/repo/worktrees/wt1/sub", // LIVE pane path, not launch dir
		"git_repo_root":   "/repo",
		"worktree":        "/repo/worktrees/wt1",
		"branch":          "feature",
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
		{Name: "alpha", UUID: "u", State: store.Working, TmuxSession: "cc-alpha", CWD: "/tmp/scratch", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	pathFn := func(_, _ string) (string, error) { return "/tmp/scratch", nil }
	out, err := renderListJSON(rows, false, "", liveFn, pathFn, nilGit, "ccpool", now, time.Hour, 24*time.Hour)
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
		{Name: "cold", UUID: "u", State: store.Done, TmuxSession: "cc-cold", CWD: "/launch/dir", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return false } // not live
	// pathFn/gitFn would report facets if called; assert they are NOT for a dead row.
	pathCalled, gitCalled := false, false
	pathFn := func(_, _ string) (string, error) { pathCalled = true; return "/should/not/be/used", nil }
	gitFn := func(string) gitfacet.Facets {
		gitCalled = true
		return gitfacet.Facets{RepoRoot: strptr("/x"), Worktree: strptr("/x"), Branch: strptr("b")}
	}
	out, _ := renderListJSON(rows, true, "", liveFn, pathFn, gitFn, "ccpool", now, time.Hour, 24*time.Hour)
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
		{Name: "alpha", UUID: "u", State: store.Working, TmuxSession: "cc-alpha", CWD: "/launch/dir", LastActivityAt: now.Unix()},
	}
	liveFn := func(_, _ string) bool { return true }
	gitFn := func(cwd string) gitfacet.Facets {
		if cwd == "/launch/dir" {
			return gitfacet.Facets{RepoRoot: strptr("/launch/dir"), Worktree: strptr("/launch/dir"), Branch: strptr("main")}
		}
		return gitfacet.Facets{}
	}
	out, _ := renderListJSON(rows, false, "", liveFn, noPath, gitFn, "ccpool", now, time.Hour, 24*time.Hour)
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
		{Name: "old-done", UUID: "u", State: store.Done, TmuxSession: "cc-x", CWD: "/repo", LastActivityAt: now.Add(-5 * time.Hour).Unix()},
	}
	liveFn := func(_, _ string) bool { return false }

	out, _ := renderListJSON(rows, false, "", liveFn, noPath, nilGit, "ccpool", now, time.Hour, 24*time.Hour)
	var def []map[string]any
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(def) != 0 {
		t.Errorf("default JSON view should hide old cold done, got %q", out)
	}

	outAll, _ := renderListJSON(rows, true, "", liveFn, noPath, nilGit, "ccpool", now, time.Hour, 24*time.Hour)
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
	pathFn := func(_, _ string) (string, error) { return "/cwd", nil }
	out, _ := renderListJSON(rows, false, "", liveFn, pathFn, nilGit, "ccpool", now, time.Hour, 24*time.Hour)
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
	emptyOut, _ := renderListJSON(nil, false, "", liveFn, pathFn, nilGit, "ccpool", now, time.Hour, 24*time.Hour)
	if strings.TrimSpace(emptyOut) != "[]" {
		t.Errorf("empty list = %q, want []", emptyOut)
	}
}
