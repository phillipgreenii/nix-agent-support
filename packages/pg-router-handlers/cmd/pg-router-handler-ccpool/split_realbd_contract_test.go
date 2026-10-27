//go:build contract

package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
)

// Real-bd regression guard for the split-triage write path (`split apply` and
// `split unsplittable`), behind the `contract` build tag (global rule V-3).
//
// WHY THIS FILE EXISTS: split_test.go and internal/beads/split_test.go drive a
// fake bd, which only impersonates the labels, edges and claim state the real bd
// reports. Whether children really carry split-from:<id>, whether `bd dep add
// <parent> <child>` really makes the parent blocked-by the child, whether a
// release really clears the assignee, and whether needs-split-review really
// disappears LAST are properties of the real bd, so they are proved here against
// it. (The cross-actor claim release is covered separately by
// internal/executor/split_release_realbd_contract_test.go; here the claimant is
// the actor running the split, the normal split-triage session.)
//
// Hermetic: dtest.NewRealBD gives a private EMBEDDED dolt store under a
// t.TempDir() (no dolt server is started or stopped, so there is no server to
// leak and no port to collide with) and an environment stripped of every
// BEADS_*, BD_*, GIT_* and WORKSPACE_ROOT binding, so nothing can reach the
// shared tracker or port 25252. The test skips when bd is not on PATH.
//
// Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run Split_RealBD ./cmd/pg-router-handler-ccpool/

// splitRecordingRunner forwards every call to the real runner and remembers the
// bd argv of each, so the ORDER of writes can be asserted against the real store.
type splitRecordingRunner struct {
	inner beads.Runner
	calls [][]string
}

func (r *splitRecordingRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, slices.Clone(args))
	return r.inner.Run(ctx, args...)
}

// lastWrite is the final recorded call that is not a pure read.
func (r *splitRecordingRunner) lastWrite(t *testing.T) string {
	t.Helper()
	for i := len(r.calls) - 1; i >= 0; i-- {
		a := r.calls[i]
		if a[0] == "show" || (a[0] == "dep" && len(a) > 1 && a[1] == "list") {
			continue
		}
		return strings.Join(a, " ")
	}
	t.Fatal("no write call recorded")
	return ""
}

// argvOf returns the argv of the first recorded call named cmd whose joined text
// contains sub.
func (r *splitRecordingRunner) argvOf(cmd, sub string) []string {
	for _, a := range r.calls {
		if a[0] == cmd && strings.Contains(strings.Join(a, " "), sub) {
			return a
		}
	}
	return nil
}

// newSplitParent files a needs-split-review bead (carrying a budget stop and an
// ordinary label, claimed by the split triager) in the real store.
func newSplitParent(t *testing.T, ctx context.Context, r *beads.CLIRunner, title string) string {
	t.Helper()
	out, err := r.Run(ctx, "q", title, "-l", "needs-split-review", "-l", "budget-stop:old-session", "-l", "area-x")
	if err != nil {
		t.Fatalf("bd q: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatal("bd q returned no id")
	}
	if out, err := r.Run(ctx, "update", id, "--claim"); err != nil {
		t.Fatalf("bd update --claim: %v\n%s", err, out)
	}
	iss := mustShowSplit(t, ctx, r, id)
	if iss.Status != "in_progress" || iss.Assignee != "split-actor" {
		t.Fatalf("fixture parent not claimed as expected: status=%q assignee=%q", iss.Status, iss.Assignee)
	}
	return id
}

func mustShowSplit(t *testing.T, ctx context.Context, r beads.Runner, id string) beads.Issue {
	t.Helper()
	iss, err := beads.ShowObj(ctx, r, id)
	if err != nil {
		t.Fatalf("bd show %s: %v", id, err)
	}
	return iss
}

func TestSplit_RealBD_Apply(t *testing.T) {
	ctx, real := dtest.NewRealBD(t, "split-actor")
	parent := newSplitParent(t, ctx, real, "needs a split")
	rec := &splitRecordingRunner{inner: real}

	plan := `{"rationale":"two independent parts","children":[` +
		`{"title":"part one","description":"do the first thing","acceptance":"first thing done"},` +
		`{"title":"part two","description":"do the second thing","acceptance":"second thing done","priority":"1"}]}`
	var out, errb bytes.Buffer
	if rc := runSplitWith([]string{"apply", parent}, strings.NewReader(plan), &out, &errb, rec); rc != 0 {
		t.Fatalf("split apply rc=%d stderr=%s", rc, errb.String())
	}
	children := strings.Fields(out.String())
	if len(children) != 2 {
		t.Fatalf("want 2 child ids on stdout, got %q", out.String())
	}

	// Children: labelled split-from:<parent>, the parent's ordinary label kept, and
	// none of the parent's own state (stop history, split-review marker) inherited.
	for _, c := range children {
		iss := mustShowSplit(t, ctx, real, c)
		if !iss.HasLabel("split-from:"+parent) || !iss.HasLabel("area-x") {
			t.Errorf("child %s labels = %v, want split-from:%s and area-x", c, iss.Labels, parent)
		}
		if iss.HasLabel("budget-stop:old-session") || iss.HasLabel(beads.LabelNeedsSplitReview) || iss.HasLabel("human") {
			t.Errorf("child %s inherited parent state: %v", c, iss.Labels)
		}
	}

	// Parent: blocked-by every child (real blocks edges), was-split, claim
	// released, needs-split-review gone.
	p := mustShowSplit(t, ctx, real, parent)
	for _, c := range children {
		if !slices.ContainsFunc(p.Dependencies, func(d beads.Dependency) bool {
			return d.ID == c && d.DependencyType == "blocks"
		}) {
			t.Errorf("parent %s lacks a blocks edge to child %s: %+v", parent, c, p.Dependencies)
		}
	}
	if !p.HasOpenBlocker() {
		t.Errorf("parent %s should be blocked by its open children", parent)
	}
	if !p.HasLabel(beads.LabelWasSplit) {
		t.Errorf("parent labels = %v, want was-split", p.Labels)
	}
	if p.HasLabel(beads.LabelNeedsSplitReview) {
		t.Errorf("parent still carries needs-split-review: %v", p.Labels)
	}
	if p.Status != "open" || p.Assignee != "" {
		t.Errorf("claim not released: status=%q assignee=%q", p.Status, p.Assignee)
	}
	if want := "update " + parent + " --remove-label needs-split-review"; rec.lastWrite(t) != want {
		t.Errorf("needs-split-review must be removed last; last write = %q, want %q", rec.lastWrite(t), want)
	}
	if a := rec.argvOf("create", "part two"); !slices.Contains(a, "--priority=1") {
		t.Errorf("child priority not passed to bd create: %v", a)
	}

	// A second round is refused (already split) and writes nothing.
	n := len(rec.calls)
	out.Reset()
	errb.Reset()
	if rc := runSplitWith([]string{"apply", parent}, strings.NewReader(plan), &out, &errb, rec); rc == 0 {
		t.Fatalf("second split apply succeeded, want refusal; stdout=%s", out.String())
	}
	for _, a := range rec.calls[n:] {
		if a[0] != "show" {
			t.Errorf("refused split wrote to the store: %v", a)
		}
	}
}

func TestSplit_RealBD_Unsplittable(t *testing.T) {
	ctx, real := dtest.NewRealBD(t, "split-actor")
	parent := newSplitParent(t, ctx, real, "cannot be split")
	rec := &splitRecordingRunner{inner: real}

	var out, errb bytes.Buffer
	if rc := runSplitWith([]string{"unsplittable", parent}, strings.NewReader("one indivisible change"), &out, &errb, rec); rc != 0 {
		t.Fatalf("split unsplittable rc=%d stderr=%s", rc, errb.String())
	}

	p := mustShowSplit(t, ctx, real, parent)
	if !p.HasLabel("human") {
		t.Errorf("parent labels = %v, want human", p.Labels)
	}
	if p.HasLabel(beads.LabelNeedsSplitReview) || p.HasLabel(beads.LabelWasSplit) {
		t.Errorf("parent labels = %v, want no needs-split-review and no was-split", p.Labels)
	}
	if p.Status != "open" || p.Assignee != "" {
		t.Errorf("claim not released: status=%q assignee=%q", p.Status, p.Assignee)
	}
	if len(p.Dependencies) != 0 {
		t.Errorf("unsplittable must create no edges: %+v", p.Dependencies)
	}
	comments, err := real.Run(ctx, "comments", parent)
	if err != nil {
		t.Fatalf("bd comments: %v", err)
	}
	// bd word-wraps the comment text for display, so compare whitespace-normalized.
	flat := strings.Join(strings.Fields(comments), " ")
	if !strings.Contains(flat, "not splittable") || !strings.Contains(flat, "one indivisible change") {
		t.Errorf("reason comment missing from bd comments:\n%s", comments)
	}
	if want := "update " + parent + " --remove-label needs-split-review"; rec.lastWrite(t) != want {
		t.Errorf("needs-split-review must be removed last; last write = %q, want %q", rec.lastWrite(t), want)
	}
}
