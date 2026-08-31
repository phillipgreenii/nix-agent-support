package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
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

// TestRoute_runRoleJSON covers args.go's parseRunRoleArgs --json handling
// (Task 1.5b): --json is accepted wherever it occurs relative to the role/bead
// positionals, and defaults to false when absent.
func TestRoute_runRoleJSON(t *testing.T) {
	if r := route([]string{"pr-pool", "run-role", "--json", "worker", "zr-1"}); r.kind != routeRunRole || r.role != "worker" || r.bead != "zr-1" || !r.json {
		t.Errorf("route(run-role --json worker zr-1) = %+v, want routeRunRole role=worker bead=zr-1 json=true", r)
	}
	if r := route([]string{"pr-pool", "run-role", "worker", "zr-1", "--json"}); r.kind != routeRunRole || r.role != "worker" || r.bead != "zr-1" || !r.json {
		t.Errorf("route(run-role worker zr-1 --json) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pr-pool", "run-role", "worker", "zr-1"}); r.json {
		t.Errorf("route(run-role worker zr-1) = %+v, want json=false when --json is absent", r)
	}
	for _, want := range []string{"run-role", "--json"} {
		if !strings.Contains(usageLine, want) {
			t.Errorf("usageLine does not mention %q", want)
		}
	}
	if !strings.Contains(helpText, "run-role [--json]") {
		t.Error("helpText does not advertise run-role [--json]")
	}
}

// TestRoute_runQueryJSON is TestRoute_runRoleJSON's run-query counterpart.
func TestRoute_runQueryJSON(t *testing.T) {
	if r := route([]string{"pr-pool", "run-query", "--json", "worker"}); r.kind != routeRunQuery || r.role != "worker" || !r.json {
		t.Errorf("route(run-query --json worker) = %+v, want routeRunQuery role=worker json=true", r)
	}
	if r := route([]string{"pr-pool", "run-query", "worker", "--json"}); r.kind != routeRunQuery || r.role != "worker" || !r.json {
		t.Errorf("route(run-query worker --json) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pr-pool", "run-query", "worker"}); r.json {
		t.Errorf("route(run-query worker) = %+v, want json=false when --json is absent", r)
	}
	if !strings.Contains(helpText, "run-query [--json]") {
		t.Error("helpText does not advertise run-query [--json]")
	}
}

// TestRenderRunRoleJSON covers renderRunRoleJSON's pure output shape: role,
// bead, and accepted=true, with no schemaVersion field (Task 0.4's
// unversioned-by-default wire decision, docs/decisions/cli.md's DEC-CLI-1
// "--json's versioning" note).
func TestRenderRunRoleJSON(t *testing.T) {
	var b bytes.Buffer
	renderRunRoleJSON(&b, "worker", "zr-9")

	var got runRoleReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.Role != "worker" || got.Bead != "zr-9" || !got.Accepted {
		t.Errorf("report = %+v, want role=worker bead=zr-9 accepted=true", got)
	}
	if bytes.Contains(b.Bytes(), []byte("schemaVersion")) {
		t.Errorf("run-role --json must not carry a schemaVersion field (unversioned, Task 0.4); got:\n%s", b.String())
	}
}

// TestRenderRunQueryJSON / TestRenderRunQueryText cover run-query's two pure
// output renderers against the SAME matches, so the two forms are proven to
// report the identical result set.
func TestRenderRunQueryJSON(t *testing.T) {
	matches := []runQueryMatch{
		{ID: "zr-1", Type: "review-pr", Title: "Review PR #1"},
		{ID: "zr-2", Type: "review-pr", Title: "Review PR #2"},
	}
	var b bytes.Buffer
	renderRunQueryJSON(&b, "worker", 3, matches)

	var got runQueryReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.Role != "worker" || got.Queries != 3 || got.Total != 2 {
		t.Errorf("report = %+v, want role=worker queries=3 total=2", got)
	}
	if len(got.Matches) != 2 || got.Matches[0] != matches[0] || got.Matches[1] != matches[1] {
		t.Errorf("matches = %+v, want %+v", got.Matches, matches)
	}
	if bytes.Contains(b.Bytes(), []byte("schemaVersion")) {
		t.Errorf("run-query --json must not carry a schemaVersion field (unversioned, Task 0.4); got:\n%s", b.String())
	}
}

func TestRenderRunQueryText(t *testing.T) {
	matches := []runQueryMatch{{ID: "zr-1", Type: "review-pr", Title: "Review PR #1"}}
	var b bytes.Buffer
	renderRunQueryText(&b, "worker", 3, matches)
	out := b.String()
	if !strings.Contains(out, "zr-1\treview-pr\tReview PR #1") {
		t.Errorf("output missing the tab-separated match line; got:\n%s", out)
	}
	if !strings.Contains(out, "# 1 worker dispatch(es) from 3 quer(ies)") {
		t.Errorf("output missing the summary line; got:\n%s", out)
	}
}

// TestRenderRunQueryJSON_emptyMatchesIsAnEmptyArray: a role with no matches
// still gets a valid "matches":[] array on the wire, never a bare `null` a
// naive consumer would have to special-case.
func TestRenderRunQueryJSON_emptyMatchesIsAnEmptyArray(t *testing.T) {
	var b bytes.Buffer
	renderRunQueryJSON(&b, "worker", 0, []runQueryMatch{})
	if !strings.Contains(b.String(), `"matches":[]`) {
		t.Errorf(`output = %s, want "matches":[]`, b.String())
	}
}
