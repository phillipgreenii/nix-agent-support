package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/query"
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

// TestRoute_runQueryJSON is TestRoute_runRoleJSON's run-query counterpart,
// using the Task 1.5c "query:<name>" grammar.
func TestRoute_runQueryJSON(t *testing.T) {
	if r := route([]string{"pr-pool", "run-query", "--json", "query:feedback-ready"}); r.kind != routeRunQuery || r.query != "feedback-ready" || r.role != "" || !r.json {
		t.Errorf("route(run-query --json query:feedback-ready) = %+v, want routeRunQuery query=feedback-ready role=\"\" json=true", r)
	}
	if r := route([]string{"pr-pool", "run-query", "query:feedback-ready", "--json"}); r.kind != routeRunQuery || r.query != "feedback-ready" || !r.json {
		t.Errorf("route(run-query query:feedback-ready --json) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pr-pool", "run-query", "query:feedback-ready"}); r.json {
		t.Errorf("route(run-query query:feedback-ready) = %+v, want json=false when --json is absent", r)
	}
	if !strings.Contains(helpText, "run-query [--json]") {
		t.Error("helpText does not advertise run-query [--json]")
	}
}

// TestHelpText_mentionsTestModeAndPerInvocationWarning covers the
// operator-command-surface rule (repo CLAUDE.md): a change to what the
// operator types or runs updates helpText's PR_POOL_* env list and its
// per-invocation-selector warning in the same commit (pattern:
// push_inject_test.go's helpText-mentions tests).
func TestHelpText_mentionsTestModeAndPerInvocationWarning(t *testing.T) {
	if !strings.Contains(helpText, "PR_POOL_TEST_MODE") {
		t.Error("helpText does not mention PR_POOL_TEST_MODE")
	}
	if !strings.Contains(usageLine, "run-query [--json] query:<name>") {
		t.Error("usageLine does not advertise run-query's query:<name> grammar")
	}
	if !strings.Contains(helpText, "MUST NOT") || !strings.Contains(helpText, "PER-INVOCATION") {
		t.Error("helpText does not warn that PR_POOL_ONLY/PR_POOL_DISABLE are per-invocation and must not be exported persistently")
	}
}

// TestRoute_runQueryLegacyRoleForm covers the deprecated bare-role form
// (Task 1.5c): a token with no "query:" prefix parses into .role, not
// .query, leaving the mapping-diagnostic decision to the handler.
func TestRoute_runQueryLegacyRoleForm(t *testing.T) {
	r := route([]string{"pr-pool", "run-query", "worker"})
	if r.kind != routeRunQuery || r.role != "worker" || r.query != "" {
		t.Errorf("route(run-query worker) = %+v, want routeRunQuery role=worker query=\"\"", r)
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
	renderRunQueryJSON(&b, "feedback-ready", matches)

	var got runQueryReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.Query != "feedback-ready" || got.Total != 2 {
		t.Errorf("report = %+v, want query=feedback-ready total=2", got)
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
	renderRunQueryText(&b, "feedback-ready", matches)
	out := b.String()
	if !strings.Contains(out, "zr-1\treview-pr\tReview PR #1") {
		t.Errorf("output missing the tab-separated match line; got:\n%s", out)
	}
	if !strings.Contains(out, "# 1 event(s) from source feedback-ready") {
		t.Errorf("output missing the summary line; got:\n%s", out)
	}
}

// TestRenderRunQueryJSON_emptyMatchesIsAnEmptyArray: a source with no matches
// still gets a valid "matches":[] array on the wire, never a bare `null` a
// naive consumer would have to special-case.
func TestRenderRunQueryJSON_emptyMatchesIsAnEmptyArray(t *testing.T) {
	var b bytes.Buffer
	renderRunQueryJSON(&b, "feedback-ready", []runQueryMatch{})
	if !strings.Contains(b.String(), `"matches":[]`) {
		t.Errorf(`output = %s, want "matches":[]`, b.String())
	}
}

// TestSetTestMode covers the "smoke commands set PR_POOL_TEST_MODE=1"
// contract (Task 1.5c) at the unit level, independent of the full
// config.Load/precheck plumbing runRunRole/runRunQuery need.
func TestSetTestMode(t *testing.T) {
	t.Setenv(envTestMode, "")
	_ = os.Unsetenv(envTestMode)
	setTestMode()
	if got := os.Getenv(envTestMode); got != "1" {
		t.Errorf("PR_POOL_TEST_MODE = %q, want %q", got, "1")
	}
}

// TestFindSource / TestSourceNames are run-query's query.SourceSet
// counterparts to TestResolveRole / TestRoleNames.
func TestFindSource(t *testing.T) {
	ss := query.SourceSet{{Name: "feedback-ready"}, {Name: "worker-ready"}}
	if s, ok := findSource(ss, "feedback-ready"); !ok || s.Name != "feedback-ready" {
		t.Errorf("feedback-ready should resolve; ok=%v s=%+v", ok, s)
	}
	if _, ok := findSource(ss, "bogus"); ok {
		t.Errorf("unknown source must not resolve")
	}
}

func TestSourceNames(t *testing.T) {
	ss := query.SourceSet{{Name: "feedback-ready"}, {Name: "worker-ready"}}
	if got := sourceNames(ss); got != "feedback-ready, worker-ready" {
		t.Errorf("sourceNames = %q, want %q", got, "feedback-ready, worker-ready")
	}
}

// TestMappingDiagnostic reproduces the Task 1.5c "Produces" contract's exact
// diagnostic text shape ('worker' is a role; run-query now names a source
// (try: query:feedback-ready)) for a role fed by a source of that name, and
// checks the fallback placeholder when the role has no feeding source at all.
func TestMappingDiagnostic(t *testing.T) {
	cfg := config.Config{
		Roles: roles.RoleSet{{Name: "worker", Binds: []string{"work.ready"}}},
		Queries: query.SourceSet{{Name: "feedback-ready", Query: query.CommandQuery{
			Meta: query.Meta{EmitTypes: []string{"work.ready"}},
		}}},
	}
	role, ok := resolveRole(cfg.Roles, "worker")
	if !ok {
		t.Fatal("worker should resolve")
	}
	want := "'worker' is a role; run-query now names a source (try: query:feedback-ready)"
	if got := mappingDiagnostic(cfg, role); got != want {
		t.Errorf("mappingDiagnostic = %q, want %q", got, want)
	}

	unfed := roles.Role{Name: "lonely", Binds: []string{"nothing.feeds.this"}}
	want2 := "'lonely' is a role; run-query now names a source (try: query:<name>)"
	if got := mappingDiagnostic(cfg, unfed); got != want2 {
		t.Errorf("mappingDiagnostic(unfed) = %q, want %q", got, want2)
	}
}
