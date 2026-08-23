package prview

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// This file pins the `--json` machine-readable contract for the consolidated
// View (pg2-4dz88.5.5). --json is a COMMITTED contract, not an open question:
// cmd/pg-pr/pr_list.go's own doc comment states `pr list --json` "is the read
// seam the pr-pool ACL consumes," and two existing marketplace callers already
// depend on field-level detail from this command family's --json output today
// (claude-marketplace/pg-pr/skills/pg-pr-write-pr-description/SKILL.md reads
// `body` from `pr show --json`; claude-marketplace/pg-pr/agents/
// pg-pr-review-pr-structure.md and pg-pr-review-jira-alignment.md both choose
// `pr info <n> --json` over `pr show --json` specifically to get PR metadata
// for scope/description review — the one thing `pr info` adds over `pr show`
// is the persisted-enrichment section, per cmd/pg-pr/pr.go's prInfoCmd Short
// text). Per cmd/pg-pr/pr.go's appendEnrichment (~line 171-199), the JSON
// branch of `pr info` returns early and so has NEVER actually carried
// enrichment fields — this file's fullView fixture and
// TestMarshalView_Full_EnrichmentFieldsPresent are what prove the NEW
// View-based JSON path does not repeat that bug.
//
// Golden fixtures live in testdata/pr-view-{full,empty}.json. Per
// pkg/provider/vcs/github/enrich_test.go's convention, they are hand-
// maintained (read via os.ReadFile) with no regeneration harness.

// fullView is the "every axis populated, including enrichment" fixture,
// built through Assemble (never hand-built as a View literal) so it stays
// internally consistent with Assemble's own derivations (e.g. BeadLinkItem's
// bd:// URL, the CI rollup, the freshness verdict). Values follow the
// no-employer-identifiers convention already used by prview_test.go and
// pg2-4dz88.5.3's fixtures: repo "o/r", PR number 42, logins alice/bob.
var fullView = Assemble(PRViewInput{
	PR: api.PR{
		Repo: "o/r", Number: 42, Title: "Add contributor guide",
		State: "open", Draft: false,
		Branch: "alice/add-guide", Base: "main", Author: "alice",
		URL:     "https://example.invalid/o/r/pull/42",
		HeadSHA: "abc123", BaseSHA: "def456",
		Merged: false, MergedAt: "",
		Additions: 120, Deletions: 4, ChangedFiles: 5,
		Labels:           []string{"enhancement", "needs-review"},
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		AutoMergeEnabled: false,
	},
	Store: &store.PullRequest{
		Repo: "o/r", Number: 42, Ownership: "mine",
		Kind: "feature", Size: "M", Languages: []string{"Go", "Nix"},
		Urgency: "high", UrgencyScore: 7, UrgencyReasons: []string{"label:p0", "incident-linked"},
		LastSyncedAt: fixedNow.Add(-30 * time.Second).Format(time.RFC3339),
	},
	Feedback: []store.Feedback{
		{
			ID: 1, Kind: "code-comment", Status: "open",
			Title: "Handle the error return", Body: "Please handle the error return value here.",
			AuthorLogin: "bob", AuthorKind: "human", Severity: "warning",
			File: "internal/foo/bar.go", Line: 42,
			IsOutdated: false, IsMinimized: false, ThreadResolved: false,
			DispositionAction: "replied",
			Link:              "https://example.invalid/o/r/pull/42#discussion_r1",
		},
	},
	Revisions: []store.Revision{
		{
			Seq: 1, HeadSHA: "abc123", BaseSHA: "def456",
			ObservedAt: "2026-08-20T10:00:00Z", LastSeenAt: "2026-08-20T10:05:00Z",
			CIState: "failure", CIPassed: 1, CIFailed: 1, CIPending: 0,
			GateState: "satisfied", GateStateN: 2, GateStateM: 2,
		},
	},
	LinkedTicketKeys: []string{"ABC-123"},
	BeadLinks: []beads.DepNode{
		{ID: "pg2-abc12", Title: "fix thing", Status: "open", Labels: []string{"human"}},
	},
	CIRuns: []api.CIRun{
		{Name: "unit", Status: "completed", Conclusion: "success"},
		{Name: "lint", Status: "completed", Conclusion: "failure"},
	},
	Now: fixedNow,
})

// emptyView is the "every optional axis at its absent/unknown marker, every
// collection an explicit empty array rather than an omitted key" fixture. It
// deliberately mixes two distinct absent shapes on purpose (see the bead's
// testing plan): Ownership/Enrichment are nil (no store row at all -> JSON
// null), while Feedback/Revisions/LinkedTicketKeys/BeadLinks are non-nil
// EMPTY slices (a store/lookup that was asked and reported zero -> JSON
// []) — the same nil-vs-non-nil-empty distinction prview.go's package doc
// documents, exercised here on the empty side of both shapes at once.
var emptyView = Assemble(PRViewInput{
	PR:               api.PR{Repo: "o/r", Number: 7},
	Feedback:         []store.Feedback{},
	Revisions:        []store.Revision{},
	LinkedTicketKeys: []string{},
	BeadLinks:        []beads.DepNode{},
	Now:              fixedNow,
})

func mustMarshalView(t *testing.T, v View) []byte {
	t.Helper()
	got, err := MarshalView(v)
	if err != nil {
		t.Fatalf("MarshalView: %v", err)
	}
	return got
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return raw
}

// assertJSONEqual compares two JSON documents structurally (decoded, then
// reflect.DeepEqual), not byte-for-byte. A byte compare would couple this
// test to encoding/json's exact whitespace/array-wrapping style, which
// prettier (via this repo's treefmt pre-commit hook) is free to reformat on
// every commit — a formatting-tool version bump would then fail this test
// for a reason that has nothing to do with the JSON contract. Structural
// equality is what "matches the golden fixture" actually means here; the
// golden files themselves stay plain, treefmt-formatted JSON like any other
// checked-in file, no exclude needed.
func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotDoc, wantDoc any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("unmarshal got: %v (input: %s)", err, got)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatalf("unmarshal want (golden fixture): %v (input: %s)", err, want)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("MarshalView output does not match golden fixture.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// ---------------------------------------------------------------------------
// 1. Golden compares (structural, not byte-for-byte — see assertJSONEqual).
// ---------------------------------------------------------------------------

func TestMarshalView_Full_MatchesGolden(t *testing.T) {
	got := mustMarshalView(t, fullView)
	want := readGolden(t, "pr-view-full.json")
	assertJSONEqual(t, got, want)
}

func TestMarshalView_Empty_MatchesGolden(t *testing.T) {
	got := mustMarshalView(t, emptyView)
	want := readGolden(t, "pr-view-empty.json")
	assertJSONEqual(t, got, want)
}

// ---------------------------------------------------------------------------
// 2. json.Unmarshal of the ENTIRE marshaled output must succeed. Mirrors
//    cmd/pg-pr/pr_test.go's TestPRInfo_JSONIsValid, whose whole reason for
//    existing is a past regression: text appended after a JSON object
//    corrupted `pr info --json`. MarshalView never appends anything after
//    the object, but this guard pins that property going forward.
// ---------------------------------------------------------------------------

func TestMarshalView_Full_JSONIsValid(t *testing.T) {
	got := mustMarshalView(t, fullView)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("json.Unmarshal(MarshalView(fullView)): %v (output: %s)", err, got)
	}
}

func TestMarshalView_Empty_JSONIsValid(t *testing.T) {
	got := mustMarshalView(t, emptyView)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("json.Unmarshal(MarshalView(emptyView)): %v (output: %s)", err, got)
	}
}

// ---------------------------------------------------------------------------
// 3. Enrichment fields (kind/size/languages/urgency+reasons) MUST be present
//    in the full-axis case's marshaled output. This is the test that closes
//    the latent bug: `pr info --json` has never carried these fields because
//    appendEnrichment's JSON branch returns early (cmd/pg-pr/pr.go, ~171-199).
// ---------------------------------------------------------------------------

func TestMarshalView_Full_EnrichmentFieldsPresent(t *testing.T) {
	got := mustMarshalView(t, fullView)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	enrichment, ok := doc["enrichment"].(map[string]any)
	if !ok {
		t.Fatalf(`doc["enrichment"] = %#v, want a populated object`, doc["enrichment"])
	}
	for _, field := range []string{"kind", "size", "languages", "urgency", "urgency_score", "urgency_reasons"} {
		if _, present := enrichment[field]; !present {
			t.Errorf("enrichment[%q] missing from marshaled output", field)
		}
	}
	if enrichment["kind"] != "feature" {
		t.Errorf(`enrichment["kind"] = %v, want "feature"`, enrichment["kind"])
	}
	if enrichment["urgency"] != "high" {
		t.Errorf(`enrichment["urgency"] = %v, want "high"`, enrichment["urgency"])
	}
	langs, ok := enrichment["languages"].([]any)
	if !ok || len(langs) != 2 {
		t.Errorf(`enrichment["languages"] = %v, want a 2-element array`, enrichment["languages"])
	}
	reasons, ok := enrichment["urgency_reasons"].([]any)
	if !ok || len(reasons) != 2 {
		t.Errorf(`enrichment["urgency_reasons"] = %v, want a 2-element array`, enrichment["urgency_reasons"])
	}
}

// ---------------------------------------------------------------------------
// 4. An axis with no data present — a not-yet-existing axis (Approvals) and
//    an existing-axis genuine absence (nil Ownership, no store row) — MUST
//    serialize as an explicit null/marker with the KEY STILL PRESENT. Parsed
//    as map[string]any specifically because unmarshaling into the typed View
//    struct cannot distinguish "key present with null value" from "key
//    absent" — only the generic map can.
// ---------------------------------------------------------------------------

func TestMarshalView_Empty_AbsentAxesAreExplicitNullsNotOmittedKeys(t *testing.T) {
	got := mustMarshalView(t, emptyView)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Existing axes with a genuine absence this call: nil pointers -> explicit
	// JSON null, key present.
	for _, key := range []string{"ownership", "enrichment"} {
		v, present := doc[key]
		if !present {
			t.Errorf("key %q is OMITTED from the marshaled output, want present with value null", key)
			continue
		}
		if v != nil {
			t.Errorf("doc[%q] = %#v, want explicit null", key, v)
		}
	}

	// Not-yet-existing axes: always the UnavailableAxis marker object, never
	// omitted, never a bare null (it carries a machine-readable reason).
	for _, key := range []string{"approvals", "policy_bot", "hide_wip"} {
		raw, present := doc[key]
		if !present {
			t.Fatalf("key %q is OMITTED from the marshaled output, want the unavailable marker", key)
		}
		marker, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("doc[%q] = %#v, want an object marker, not null/omitted", key, raw)
		}
		if avail, ok := marker["available"].(bool); !ok || avail {
			t.Errorf("doc[%q][\"available\"] = %v, want false", key, marker["available"])
		}
		if reason, ok := marker["reason"].(string); !ok || reason == "" {
			t.Errorf("doc[%q][\"reason\"] = %v, want a non-empty reason string", key, marker["reason"])
		}
	}

	// Zero-length collections: present as [] (not omitted, not null) —
	// distinct from the null case above.
	for _, key := range []string{"feedback", "revisions", "linked_ticket_keys", "bead_links"} {
		raw, present := doc[key]
		if !present {
			t.Fatalf("key %q is OMITTED from the marshaled output, want an empty array", key)
		}
		arr, ok := raw.([]any)
		if !ok {
			t.Fatalf("doc[%q] = %#v (%T), want a JSON array", key, raw, raw)
		}
		if len(arr) != 0 {
			t.Errorf("doc[%q] = %v, want empty", key, arr)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. The as-of time and stale flag MUST both be present in the JSON payload
//    (INV-ASOF-1, docs/behavior/invariants.md: "Every acted-on read seam MUST
//    carry its own as-of time ... An item ... with no usable as-of time MUST
//    be reported stale").
// ---------------------------------------------------------------------------

func TestMarshalView_AsOfAndStaleArePresent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		view      View
		wantAsOf  string
		wantStale bool
	}{
		{"full (fresh store row)", fullView, fullView.AsOf, false},
		{"empty (no store row -> fail-closed stale)", emptyView, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mustMarshalView(t, tc.view)
			var doc map[string]any
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			asOf, present := doc["as_of"]
			if !present {
				t.Fatalf(`key "as_of" is OMITTED from the marshaled output`)
			}
			if asOf != tc.wantAsOf {
				t.Errorf(`doc["as_of"] = %v, want %q`, asOf, tc.wantAsOf)
			}
			stale, present := doc["stale"]
			if !present {
				t.Fatalf(`key "stale" is OMITTED from the marshaled output`)
			}
			if stale != tc.wantStale {
				t.Errorf(`doc["stale"] = %v, want %v`, stale, tc.wantStale)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. PGPR_OUTPUT=json with no --json flag still yields JSON. Mirrors
//    internal/output/format_test.go's TestResolve coverage pattern (t.Setenv
//    over output.EnvVar, no flag). The CLI wiring itself (the output.Resolve
//    call site inside a cobra RunE) is a separate sibling bead's scope; this
//    proves that once resolution says "emit JSON" purely from the env var
//    with flag=false, MarshalView still produces the same valid JSON
//    document the --json flag path would.
// ---------------------------------------------------------------------------

func TestMarshalView_PGPROutputEnvSelectsJSONWithoutFlag(t *testing.T) {
	t.Setenv(output.EnvVar, "json")
	if !output.Resolve(false) {
		t.Fatalf("output.Resolve(false) = false, want true when %s=json", output.EnvVar)
	}
	got := mustMarshalView(t, fullView)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("output selected via %s=json is not valid JSON: %v", output.EnvVar, err)
	}
	if _, present := doc["identity"]; !present {
		t.Errorf(`doc["identity"] missing; want the same View JSON shape as the --json flag path`)
	}
}
