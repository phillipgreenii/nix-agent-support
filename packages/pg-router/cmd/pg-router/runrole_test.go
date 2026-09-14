package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

func TestResolveRole(t *testing.T) {
	rs := roles.RoleSet{{Name: "feedback", Enabled: true}, {Name: "worker", Enabled: true}}
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

// TestRoute_runRoleJSON covers args.go's parseRunRoleArgs --json handling
// (Task 1.5b): --json is accepted wherever it occurs relative to the
// role/event-JSON positionals, and defaults to false when absent.
func TestRoute_runRoleJSON(t *testing.T) {
	const evt = `{"id":"e","type":"t"}`
	if r := route([]string{"pg-router", "run-role", "--json", "worker", evt}); r.kind != routeRunRole || r.role != "worker" || r.eventJSON != evt || !r.json {
		t.Errorf("route(run-role --json worker %s) = %+v, want routeRunRole role=worker eventJSON=%s json=true", evt, r, evt)
	}
	if r := route([]string{"pg-router", "run-role", "worker", evt, "--json"}); r.kind != routeRunRole || r.role != "worker" || r.eventJSON != evt || !r.json {
		t.Errorf("route(run-role worker %s --json) = %+v, want the same, order-independent", evt, r)
	}
	if r := route([]string{"pg-router", "run-role", "worker", evt}); r.json {
		t.Errorf("route(run-role worker %s) = %+v, want json=false when --json is absent", evt, r)
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
	if r := route([]string{"pg-router", "run-query", "--json", "query:feedback-ready"}); r.kind != routeRunQuery || r.query != "feedback-ready" || r.role != "" || !r.json {
		t.Errorf("route(run-query --json query:feedback-ready) = %+v, want routeRunQuery query=feedback-ready role=\"\" json=true", r)
	}
	if r := route([]string{"pg-router", "run-query", "query:feedback-ready", "--json"}); r.kind != routeRunQuery || r.query != "feedback-ready" || !r.json {
		t.Errorf("route(run-query query:feedback-ready --json) = %+v, want the same, order-independent", r)
	}
	if r := route([]string{"pg-router", "run-query", "query:feedback-ready"}); r.json {
		t.Errorf("route(run-query query:feedback-ready) = %+v, want json=false when --json is absent", r)
	}
	if !strings.Contains(helpText, "run-query [--json]") {
		t.Error("helpText does not advertise run-query [--json]")
	}
}

// TestHelpText_mentionsTestModeAndPerInvocationWarning covers the
// operator-command-surface rule (repo CLAUDE.md): a change to what the
// operator types or runs updates helpText's PG_ROUTER_* env list and its
// per-invocation-selector warning in the same commit (pattern:
// push_inject_test.go's helpText-mentions tests).
func TestHelpText_mentionsTestModeAndPerInvocationWarning(t *testing.T) {
	if !strings.Contains(helpText, "PG_ROUTER_TEST_MODE") {
		t.Error("helpText does not mention PG_ROUTER_TEST_MODE")
	}
	if !strings.Contains(usageLine, "run-query [--json] query:<name>") {
		t.Error("usageLine does not advertise run-query's query:<name> grammar")
	}
	if !strings.Contains(helpText, "MUST NOT") || !strings.Contains(helpText, "PER-INVOCATION") {
		t.Error("helpText does not warn that PG_ROUTER_ONLY/PG_ROUTER_DISABLE are per-invocation and must not be exported persistently")
	}
}

// TestRoute_runQueryBareRoleFormIsUsageError covers the retired pre-Task-1.5c
// bare-role form: it is no longer a special-cased diagnostic path (operator
// ruling, 2026-09-02 — no live consumer to migrate), so a token with no
// "query:" prefix is an ordinary usage error like any other malformed
// argument.
func TestRoute_runQueryBareRoleFormIsUsageError(t *testing.T) {
	r := route([]string{"pg-router", "run-query", "worker"})
	if r.kind != routeUsageErr {
		t.Errorf("route(run-query worker) = %+v, want routeUsageErr", r)
	}
}

// TestRunRunRole_RejectsMalformedEvent covers run-role's new <json>
// positional (pg2-oju6w.15), mirroring push_inject_test.go's
// TestPushInject_RejectsMalformedEvent table: runRunRole schema-validates
// and decodes the event BEFORE loading config or resolving the role
// (runrole.go's own ordering), so every case here fails the same way
// regardless of role name or configured roles — none of these cases needs a
// real config fixture.
func TestRunRunRole_RejectsMalformedEvent(t *testing.T) {
	cases := map[string]string{
		"not json":              `{not json`,
		"missing type":          `{"schemaVersion":"1","id":"x"}`,
		"bad at":                `{"schemaVersion":"1","id":"x","type":"t","at":"yesterday"}`,
		"bad expiresAt":         `{"schemaVersion":"1","id":"x","type":"t","expiresAt":"soon"}`,
		"legacy duration field": `{"schemaVersion":"1","id":"x","type":"t","ttl":"5m"}`,
	}
	for desc, arg := range cases {
		t.Run(desc, func(t *testing.T) {
			if code := runRunRole("whatever-role", arg, false); code != exitGeneric {
				t.Fatalf("exit = %d, want %d (a malformed event must be rejected before any role/config lookup)", code, exitGeneric)
			}
		})
	}
}

// TestRenderRunRoleJSON covers renderRunRoleJSON's pure output shape: role,
// item, and accepted=true, with no schemaVersion field (Task 0.4's
// unversioned-by-default wire decision, docs/decisions/cli.md's DEC-CLI-1
// "--json's versioning" note).
func TestRenderRunRoleJSON(t *testing.T) {
	var b bytes.Buffer
	renderRunRoleJSON(&b, "worker", "zr-9")

	var got runRoleReport
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, b.String())
	}
	if got.Role != "worker" || got.Item != "zr-9" || !got.Accepted {
		t.Errorf("report = %+v, want role=worker item=zr-9 accepted=true", got)
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

// TestSetTestMode covers the "smoke commands set PG_ROUTER_TEST_MODE=1"
// contract (Task 1.5c) at the unit level, independent of the full
// config.Load/precheck plumbing runRunRole/runRunQuery need.
func TestSetTestMode(t *testing.T) {
	t.Setenv(envTestMode, "")
	_ = os.Unsetenv(envTestMode)
	setTestMode()
	if got := os.Getenv(envTestMode); got != "1" {
		t.Errorf("PG_ROUTER_TEST_MODE = %q, want %q", got, "1")
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
