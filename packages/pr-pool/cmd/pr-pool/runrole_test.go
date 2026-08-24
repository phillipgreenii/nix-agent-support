package main

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestResolveRole(t *testing.T) {
	d := config.Default()
	rs := roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir:  d.WorktreeDir,
		MaxFeedback:  d.MaxFeedback,
		MaxWorker:    d.MaxWorker,
		WorkerBudget: d.WorkerBudget(),
	})
	if r, ok := resolveRole(rs, "feedback"); !ok || r.Name != "feedback" {
		t.Errorf("feedback should resolve; ok=%v r=%+v", ok, r)
	}
	if r, ok := resolveRole(rs, "worker"); !ok || r.Name != "worker" {
		t.Errorf("worker should resolve; ok=%v r=%+v", ok, r)
	}
	if _, ok := resolveRole(rs, "bogus"); ok {
		t.Errorf("unknown role must not resolve")
	}
}

func TestRoleNames(t *testing.T) {
	rs := roles.RoleSet{{Name: "feedback"}, {Name: "worker"}}
	if got := roleNames(rs); got != "feedback, worker" {
		t.Errorf("roleNames = %q, want \"feedback, worker\"", got)
	}
}

// TestBuildRunRoleEvent_populatesMetadata is the pg2-jpci regression: the
// direct-bead run-role path must load the bead's metadata into the event's Item
// (mirroring the query/drain path), so the review prompt template renders the real
// pr_number/repo/head_sha instead of <no value>. Under the event model run-role
// builds a self-contained EVENT (design Q-meta); the dispatch context is derived
// from it. fakeBR (drain_test.go) returns the bead's `bd show <id> --json` payload.
func TestBuildRunRoleEvent_populatesMetadata(t *testing.T) {
	const beadID = "zr-vd38a"
	br := fakeBR{out: map[string]string{
		"show " + beadID + " --json": `{"data":[{"id":"zr-vd38a","issue_type":"review-pr","title":"Review PR #99116","metadata":{"pr_number":99116,"repo":"acme/widgets","head_sha":"abc123def","branch":"feature/x"}}]}`,
	}}
	role := roles.Role{Name: "review", Binds: []string{"review.ready"}}
	ev, err := buildRunRoleEvent(context.Background(), br, role, beadID)
	if err != nil {
		t.Fatalf("buildRunRoleEvent error: %v", err)
	}
	if ev.Item.ID != beadID {
		t.Errorf("Item.ID = %q, want %q", ev.Item.ID, beadID)
	}
	// The event's type is the role's bind (provenance); the context is derived.
	if ev.Type != "review.ready" {
		t.Errorf("event type = %q, want the role's bind review.ready", ev.Type)
	}
	if ev.Item.Metadata == nil {
		t.Fatalf("Item.Metadata is nil; run-role did not load bead metadata (pg2-jpci)")
	}
	for _, k := range []string{"pr_number", "repo", "head_sha"} {
		if _, ok := ev.Item.Metadata[k]; !ok {
			t.Errorf("Item.Metadata missing %q; got %#v", k, ev.Item.Metadata)
		}
	}
}
