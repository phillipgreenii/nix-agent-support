package beads

import (
	"context"
	"encoding/json"
	"testing"

	"strings"
)

// hasArg reports whether flag appears anywhere in the bd args.
func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// filterOpenTasks drops non-open issues from a bd `list --json` payload, mimicking
// what `bd list --status=open` returns from the real server.
func filterOpenTasks(raw string) string {
	if raw == "" {
		return raw
	}
	var wrap struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return raw // not the wrapped shape; leave untouched
	}
	kept := wrap.Data[:0]
	for _, iss := range wrap.Data {
		if s, _ := iss["status"].(string); s == "open" {
			kept = append(kept, iss)
		}
	}
	wrap.Data = kept
	b, _ := json.Marshal(wrap)
	return string(b)
}

// attentionRunner returns canned output per bd subcommand and records calls.
type attentionRunner struct {
	calls    [][]string
	children string // output for `dep list <id> --direction=up --json`
	tasks    string // output for `list --type=task ...`
	createID string // ID returned for `create`
}

func (r *attentionRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	switch {
	case len(args) >= 2 && args[0] == "dep" && args[1] == "list":
		return r.children, nil
	case len(args) >= 2 && args[0] == "dep" && args[1] == "add":
		return "", nil
	case len(args) >= 1 && args[0] == "list":
		// Mimic `bd list --status=open`: when the caller filters to open, a
		// closed bead is invisible (real bd would not return it).
		if hasArg(args, "--status=open") {
			return filterOpenTasks(r.tasks), nil
		}
		return r.tasks, nil
	case len(args) >= 1 && args[0] == "create":
		return r.createID, nil
	case len(args) >= 1 && args[0] == "close":
		return "", nil
	}
	return "", nil
}

func (r *attentionRunner) saw(sub string) bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

func (r *attentionRunner) createCall() []string {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "create" {
			return c
		}
	}
	return nil
}

func TestEnsureAttentionCreatesWhenNoOpenChild(t *testing.T) {
	r := &attentionRunner{children: "[]", createID: "att-1"}
	c := NewClientWithRunner(r)
	id, err := c.EnsureAttentionBead(context.Background(), "mr-1", "o/r#7")
	if err != nil {
		t.Fatalf("EnsureAttentionBead: %v", err)
	}
	if id != "att-1" {
		t.Fatalf("id = %q, want att-1", id)
	}
	if !r.saw("create") {
		t.Fatalf("expected a create, calls: %v", r.calls)
	}
	joined := strings.Join(r.createCall(), " ")
	if !strings.Contains(joined, attentionTitlePrefix+"o/r#7") {
		t.Fatalf("create title missing prefix: %v", r.createCall())
	}
	// team label (NOT mine).
	if !strings.Contains(joined, "-l team") {
		t.Fatalf("expected team label, got: %v", r.createCall())
	}
	if strings.Contains(joined, "-l mine") {
		t.Fatalf("attention bead must NOT carry the mine label: %v", r.createCall())
	}
	// parent-child link.
	if !r.saw("dep") {
		t.Fatalf("expected a parent-child dep add, calls: %v", r.calls)
	}
}

func TestEnsureAttentionDedupsOpenChild(t *testing.T) {
	// An OPEN attention child already exists → no create (idempotent open).
	r := &attentionRunner{
		children: `[{"id":"att-1"}]`,
		tasks:    `{"data":[{"id":"att-1","title":"attention: o/r#7","status":"open"}]}`,
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureAttentionBead(context.Background(), "mr-1", "o/r#7")
	if err != nil {
		t.Fatalf("EnsureAttentionBead: %v", err)
	}
	if id != "att-1" {
		t.Fatalf("id = %q, want existing att-1", id)
	}
	if r.saw("create") {
		t.Fatalf("must not create when an OPEN child exists, calls: %v", r.calls)
	}
}

func TestEnsureAttentionReopensAfterClosedWindow(t *testing.T) {
	// A CLOSED attention child exists but NO open one. Unlike draft-review (which
	// is a one-shot obligation), attention is a repeating open/close signal: the
	// ENSURE path finds only OPEN children, so a new one is created when attention
	// is needed again (re-open after a not-needed window).
	r := &attentionRunner{
		children: `[{"id":"att-old"}]`,
		tasks:    `{"data":[{"id":"att-old","title":"attention: o/r#7","status":"closed"}]}`,
		createID: "att-2",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureAttentionBead(context.Background(), "mr-1", "o/r#7")
	if err != nil {
		t.Fatalf("EnsureAttentionBead: %v", err)
	}
	if id != "att-2" {
		t.Fatalf("id = %q, want a freshly created att-2 (closed child must not suppress)", id)
	}
	if !r.saw("create") {
		t.Fatalf("expected a create to re-open attention, calls: %v", r.calls)
	}
}

func TestEnsureAttentionPropagatesLookupError(t *testing.T) {
	r := &errChildrenRunner{}
	c := NewClientWithRunner(r)
	if _, err := c.EnsureAttentionBead(context.Background(), "mr-1", "o/r#7"); err == nil {
		t.Fatal("expected a lookup error to propagate (never treated as 'none exists')")
	}
}

func TestCloseAttentionClosesOpenChild(t *testing.T) {
	r := &attentionRunner{
		children: `[{"id":"att-1"}]`,
		tasks:    `{"data":[{"id":"att-1","title":"attention: o/r#7","status":"open"}]}`,
	}
	c := NewClientWithRunner(r)
	if err := c.CloseAttentionBead(context.Background(), "mr-1", "resolved"); err != nil {
		t.Fatalf("CloseAttentionBead: %v", err)
	}
	// Must have issued a close on the open child.
	var closedID string
	for _, call := range r.calls {
		if len(call) >= 2 && call[0] == "close" {
			closedID = call[1]
		}
	}
	if closedID != "att-1" {
		t.Fatalf("expected close att-1, calls: %v", r.calls)
	}
}

func TestCloseAttentionNoOpWhenNoOpenChild(t *testing.T) {
	// No open child → nothing to close (idempotent).
	r := &attentionRunner{children: "[]"}
	c := NewClientWithRunner(r)
	if err := c.CloseAttentionBead(context.Background(), "mr-1", "resolved"); err != nil {
		t.Fatalf("CloseAttentionBead: %v", err)
	}
	if r.saw("close") {
		t.Fatalf("must not close when no open child exists, calls: %v", r.calls)
	}
}

// errChildrenRunner errors on the `dep list` (children) lookup.
type errChildrenRunner struct{}

func (errChildrenRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "dep" && args[1] == "list" {
		return "", errAttnBoom
	}
	return "", nil
}

var errAttnBoom = errAttnString("boom")

type errAttnString string

func (e errAttnString) Error() string { return string(e) }
