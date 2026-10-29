package fakebd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// bdRun is one in-process invocation of the fake against a workspace dir.
type bdRun struct {
	t   *testing.T
	dir string
	now time.Time
}

func (b *bdRun) run(args ...string) (stdout, stderr string, code int) {
	b.t.Helper()
	var out, errb bytes.Buffer
	b.now = b.now.Add(time.Second)
	now := b.now
	code = Main(Env{Args: args, Dir: b.dir, Stdout: &out, Stderr: &errb, Now: func() time.Time { return now }})
	return out.String(), errb.String(), code
}

func (b *bdRun) ok(args ...string) string {
	b.t.Helper()
	out, errs, code := b.run(args...)
	if code != 0 {
		b.t.Fatalf("bd %v exited %d: %s%s", args, code, out, errs)
	}
	return out
}

func data[T any](t *testing.T, out string) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not a --json envelope: %v\n%s", err, out)
	}
	return env.Data
}

func newWorkspace(t *testing.T) *bdRun {
	t.Helper()
	b := &bdRun{t: t, dir: t.TempDir(), now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	b.ok("init", "--prefix", "pc", "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	return b
}

func TestCreateListShowInTheShapesTheLaneReads(t *testing.T) {
	b := newWorkspace(t)
	anchor := strings.TrimSpace(b.ok("create", "acme/api#1: x", "-t", "merge-request", "--silent", "--metadata", `{"repo":"acme/api","pr_number":"1"}`))
	if anchor != "pc-1" {
		t.Fatalf("the first id is %q, want pc-1", anchor)
	}
	// The beads backend's create: flags, -C and --actor before the verb, a JSON object back.
	created := data[Issue](t, b.ok("-C", b.dir, "--actor", "lane", "create", "--title", "review-pr: acme/api#1", "--json",
		"--type", "task", "--parent", anchor, "--labels", "b,a", "--description", "d"))
	if created.ID != "pc-1.1" || created.Parent != anchor || created.CreatedBy != "lane" || strings.Join(created.Labels, ",") != "a,b" {
		t.Fatalf("created %+v", created)
	}
	shown := data[[]Issue](t, b.ok("show", "--readonly", "--json", "--", created.ID))
	if len(shown) != 1 || shown[0].Title != "review-pr: acme/api#1" || shown[0].Status != "open" || shown[0].Priority != 2 {
		t.Fatalf("show %+v", shown)
	}
	b.ok("close", created.ID, "--reason", "done", "--force")
	if got := data[[]Issue](t, b.ok("list", "--json")); len(got) != 1 || got[0].ID != anchor {
		t.Errorf("a plain list must leave the closed item out: %+v", got)
	}
	if got := data[[]Issue](t, b.ok("list", "--all", "--limit", "0", "--json")); len(got) != 2 {
		t.Errorf("--all lists %d items, want 2", len(got))
	}
	got := data[[]Issue](t, b.ok("list", "--type", "task", "--title-contains", "review-pr:", "--status", "open,closed", "--json", "--limit", "0"))
	if len(got) != 1 || got[0].ID != created.ID || got[0].ClosedAt == "" || got[0].CloseReason != "done" {
		t.Errorf("the filtered list: %+v", got)
	}
	if got := data[[]Issue](t, b.ok("list", "--parent="+anchor, "--readonly", "--json", "--limit", "0", "--all")); len(got) != 1 {
		t.Errorf("the children of the anchor: %+v", got)
	}
	if got := data[[]Issue](t, b.ok("list", "--all", "--json", "--metadata-field", "pr_number=1", "--sort", "created")); len(got) != 1 || got[0].ID != anchor {
		t.Errorf("the metadata filter: %+v", got)
	}
}

func TestUpdateCoercesMetadataAndGuards(t *testing.T) {
	b := newWorkspace(t)
	id := strings.TrimSpace(b.ok("create", "t", "--silent"))
	before := data[[]Issue](t, b.ok("show", id, "--json"))[0]
	it := data[[]Issue](t, b.ok("update", "--set-metadata", "n=42", "--set-metadata", "f=true", "--set-metadata", "s=0123", "--add-label", "x", "--json", "--", id))[0]
	if string(it.Metadata["n"]) != "42" || string(it.Metadata["f"]) != "true" || string(it.Metadata["s"]) != `"0123"` {
		t.Errorf("metadata %v: a number and a boolean are stored as such, anything else as a string", it.Metadata)
	}
	if it.UpdatedAt == before.UpdatedAt {
		t.Error("an update did not move updated_at")
	}
	b.ok("update", id, "--unset-metadata", "n", "--remove-label", "x", "--status", "in_progress", "--assignee", "reviewer")
	if _, _, code := b.run("update", "--if-status", "in_progress", "--if-assignee", "someone-else", "--title", "no", "--json", "--", id); code != 1 {
		t.Errorf("a failed guard exited %d, want 1", code)
	}
	it = data[[]Issue](t, b.ok("show", id, "--json"))[0]
	if it.Title != "t" || it.Assignee != "reviewer" || it.Status != "in_progress" || it.Metadata["n"] != nil || len(it.Labels) != 0 {
		t.Errorf("after the refused update: %+v", it)
	}
	b.ok("update", id, "--assignee=")
	if it = data[[]Issue](t, b.ok("show", id, "--json"))[0]; it.Assignee != "" {
		t.Errorf("--assignee= did not clear the assignee: %q", it.Assignee)
	}
}

func TestCommentsKeepTheirOrderWithinOneSecond(t *testing.T) {
	b := newWorkspace(t)
	id := strings.TrimSpace(b.ok("create", "t", "--silent"))
	for i := 0; i < 11; i++ {
		b.ok("comment", "--json", "--", id, "body")
	}
	b.ok("comments", "add", id, "note")
	cs := data[[]Comment](t, b.ok("comments", id, "--json"))
	if len(cs) != 12 || cs[11].Text != "note" {
		t.Fatalf("comments %+v", cs)
	}
	for i := 1; i < len(cs); i++ {
		if cs[i-1].ID >= cs[i].ID {
			t.Errorf("comment ids %q then %q do not sort in the order they were made", cs[i-1].ID, cs[i].ID)
		}
	}
	if it := data[[]Issue](t, b.ok("show", id, "--json"))[0]; it.CommentCount != 12 {
		t.Errorf("comment_count %d, want 12", it.CommentCount)
	}
}

func TestReopenAndCloseKeepClosedAtInStep(t *testing.T) {
	b := newWorkspace(t)
	id := strings.TrimSpace(b.ok("create", "t", "--silent"))
	if _, _, code := b.run("reopen", id); code != 1 {
		t.Errorf("reopening an open item exited %d, want 1", code)
	}
	b.ok("close", "--reason", "r", "--force", "--json", "--", id)
	b.ok("reopen", id, "--reason", "back")
	it := data[[]Issue](t, b.ok("show", id, "--json"))[0]
	if it.Status != "open" || it.ClosedAt != "" || it.CloseReason != "" {
		t.Errorf("after the reopen: %+v", it)
	}
}

func TestUnknownIdsVerbsAndFlagsFailLoudly(t *testing.T) {
	b := newWorkspace(t)
	out, errs, code := b.run("show", "--json", "--", "pc-404")
	if code != 1 || !strings.Contains(errs, `no issue found matching "pc-404"`) {
		t.Errorf("an unknown id: exit %d, stderr %q", code, errs)
	}
	if e := data[map[string]string](t, out); e["error"] == "" {
		t.Errorf("an unknown id under --json carries no error envelope: %q", out)
	}
	for _, args := range [][]string{
		{"ready"},
		{"list", "--no-such-flag"},
		{"update", "pc-1", "--defer", "2026-11-01"},
		{"create"},
		{"list", "--sort", "priority"},
	} {
		if _, _, code := b.run(args...); code != 1 {
			t.Errorf("bd %v exited %d, want 1", args, code)
		}
	}
	if _, _, code := (&bdRun{t: t, dir: t.TempDir()}).run("list", "--json"); code != 1 {
		t.Errorf("a list outside a workspace exited %d, want 1", code)
	}
}

func TestDepListAndSearch(t *testing.T) {
	b := newWorkspace(t)
	id := strings.TrimSpace(b.ok("create", "Cache the lookups", "--silent"))
	if got := data[[]any](t, b.ok("dep", "list", "--direction", "down", "--type", "blocks", "--json", "--", id)); len(got) != 0 {
		t.Errorf("dep list %v, want none", got)
	}
	if got := data[[]Issue](t, b.ok("search", "--readonly", "--json", "-n", "50", "--query=cache")); len(got) != 1 {
		t.Errorf("search found %d, want 1", len(got))
	}
}
