package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// draftReviewRunner returns canned output per bd subcommand and records calls.
// (Named to avoid colliding with scriptedRunner in processingcycle_test.go.)
type draftReviewRunner struct {
	calls    [][]string
	children string // output for `dep list <id> --direction=up --json`
	tasks    string // output for `list --type=task ...`
	tasksErr error  // if set, the `list --type=task` call errors
	createID string // ID returned for `create`
}

func (r *draftReviewRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	switch {
	case len(args) >= 2 && args[0] == "dep" && args[1] == "list":
		return r.children, nil
	case len(args) >= 2 && args[0] == "dep" && args[1] == "add":
		return "", nil
	case len(args) >= 1 && args[0] == "list":
		if r.tasksErr != nil {
			return "", r.tasksErr
		}
		return r.tasks, nil
	case len(args) >= 1 && args[0] == "create":
		return r.createID, nil
	}
	return "", nil
}

func (r *draftReviewRunner) sawCreate() bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "create" {
			return true
		}
	}
	return false
}

func (r *draftReviewRunner) sawDepAdd() bool {
	for _, c := range r.calls {
		if len(c) >= 2 && c[0] == "dep" && c[1] == "add" {
			return true
		}
	}
	return false
}

func TestEnsureDraftReviewCreatesWhenNoChild(t *testing.T) {
	r := &draftReviewRunner{children: "[]", createID: "dr-1"}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", true)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want dr-1", id)
	}
	if !r.sawCreate() {
		t.Fatalf("expected a create, calls: %v", r.calls)
	}
	if !r.sawDepAdd() {
		t.Fatalf("expected a parent-child dep add, calls: %v", r.calls)
	}
	// Title carries the prefix; mine adds the label.
	var createCall []string
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "create" {
			createCall = call
		}
	}
	joined := strings.Join(createCall, " ")
	if !strings.Contains(joined, draftReviewTitlePrefix+"o/r#7") {
		t.Fatalf("create title missing prefix: %v", createCall)
	}
	if !strings.Contains(joined, "-l mine") {
		t.Fatalf("expected mine label, got: %v", createCall)
	}
	if !strings.Contains(joined, "--silent") {
		t.Fatalf("expected --silent flag, got: %v", createCall)
	}
}

func TestEnsureDraftReviewDedupsExistingChild(t *testing.T) {
	// An open draft-review child already exists → no create.
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#7","status":"open"}]}`,
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want existing dr-1", id)
	}
	if r.sawCreate() {
		t.Fatalf("must not create when a child exists, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewDoesNotResurrectClosedChild(t *testing.T) {
	// A CLOSED draft-review child exists (visible because we list --all) → no create.
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#7","status":"closed"}]}`,
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want closed dr-1 (no resurrection)", id)
	}
	if r.sawCreate() {
		t.Fatalf("must not resurrect a closed child, calls: %v", r.calls)
	}
}

func (r *draftReviewRunner) sawUpdate(id string) bool {
	for _, c := range r.calls {
		if len(c) >= 1 && c[0] == "update" && len(c) >= 2 && c[1] == id {
			return true
		}
	}
	return false
}

func TestEnsureDraftReviewMineLabel_AddsWhenMissing(t *testing.T) {
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#1","status":"open","labels":[]}]}`,
	}
	c := NewClientWithRunner(r)
	if err := c.EnsureDraftReviewMineLabel(context.Background(), "mr-1"); err != nil {
		t.Fatalf("EnsureDraftReviewMineLabel: %v", err)
	}
	if !r.sawUpdate("dr-1") {
		t.Fatalf("expected `update dr-1 --add-label mine`, calls: %v", r.calls)
	}
	var updateCall []string
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "update" {
			updateCall = call
		}
	}
	joined := strings.Join(updateCall, " ")
	if !strings.Contains(joined, "--add-label mine") {
		t.Fatalf("expected --add-label mine, got: %v", updateCall)
	}
}

func TestEnsureDraftReviewMineLabel_NoopWhenPresent(t *testing.T) {
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#1","status":"open","labels":["mine"]}]}`,
	}
	c := NewClientWithRunner(r)
	if err := c.EnsureDraftReviewMineLabel(context.Background(), "mr-1"); err != nil {
		t.Fatalf("EnsureDraftReviewMineLabel: %v", err)
	}
	if r.sawUpdate("dr-1") {
		t.Fatalf("must not update when mine label already present, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewMineLabel_PropagatesLookupError(t *testing.T) {
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasksErr: errors.New("boom"),
	}
	c := NewClientWithRunner(r)
	err := c.EnsureDraftReviewMineLabel(context.Background(), "mr-1")
	if err == nil {
		t.Fatal("expected lookup error to propagate, got nil")
	}
	if r.sawUpdate("dr-1") {
		t.Fatalf("must not update on lookup error, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewMineLabel_NoopWhenNoChildren(t *testing.T) {
	r := &draftReviewRunner{children: "[]"}
	c := NewClientWithRunner(r)
	if err := c.EnsureDraftReviewMineLabel(context.Background(), "mr-1"); err != nil {
		t.Fatalf("EnsureDraftReviewMineLabel: %v", err)
	}
	if r.sawUpdate("dr-1") {
		t.Fatalf("must not update when there are no children, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewMineLabel_RequiresPRBeadID(t *testing.T) {
	r := &draftReviewRunner{}
	c := NewClientWithRunner(r)
	if err := c.EnsureDraftReviewMineLabel(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty prBeadID, got nil")
	}
}

func TestEnsureDraftReviewPropagatesLookupError(t *testing.T) {
	// Children exist, but the task-list lookup errors → must NOT create.
	r := &draftReviewRunner{
		children: `[{"id":"dr-1"}]`,
		tasksErr: errors.New("boom"),
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	_, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err == nil {
		t.Fatal("expected lookup error to propagate, got nil (would risk a duplicate bead)")
	}
	if r.sawCreate() {
		t.Fatalf("must not create on lookup error, calls: %v", r.calls)
	}
}
