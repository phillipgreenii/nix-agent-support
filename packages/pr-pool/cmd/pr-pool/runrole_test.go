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

// TestBuildRunRoleDispatch_populatesMetadata is the pg2-jpci regression: the
// direct-bead run-role path must load the bead's metadata into the dispatched Item
// (mirroring the query/drain path), so the review prompt template renders the real
// pr_number/repo/head_sha instead of <no value>. fakeBR (drain_test.go) returns the
// bead's `bd show <id> --json` payload with populated metadata.
func TestBuildRunRoleDispatch_populatesMetadata(t *testing.T) {
	const beadID = "zr-vd38a"
	br := fakeBR{out: map[string]string{
		"show " + beadID + " --json": `{"data":[{"id":"zr-vd38a","issue_type":"review-pr","title":"Review PR #99116","metadata":{"pr_number":99116,"repo":"ziprecruiter/ziprecruiter","head_sha":"abc123def","branch":"feature/x"}}]}`,
	}}
	dctx, err := buildRunRoleDispatch(context.Background(), br, roles.Role{Name: "review"}, beadID)
	if err != nil {
		t.Fatalf("buildRunRoleDispatch error: %v", err)
	}
	if dctx.Item.ID != beadID {
		t.Errorf("Item.ID = %q, want %q", dctx.Item.ID, beadID)
	}
	if dctx.Item.Metadata == nil {
		t.Fatalf("Item.Metadata is nil; run-role did not load bead metadata (pg2-jpci)")
	}
	for _, k := range []string{"pr_number", "repo", "head_sha"} {
		if _, ok := dctx.Item.Metadata[k]; !ok {
			t.Errorf("Item.Metadata missing %q; got %#v", k, dctx.Item.Metadata)
		}
	}
}
