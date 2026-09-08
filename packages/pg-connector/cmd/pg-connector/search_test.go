package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// writeSearchConfigFor writes a top-level search.sources registry listing
// backends (in order) and points $PG_PR_CONFIG at it. search.sources is
// always list-valued and independent of connector.<type> (registry.go),
// mirroring writeAttentionConfigFor's own convention for attention.sources.
func writeSearchConfigFor(t *testing.T, backends ...string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	var sb strings.Builder
	sb.WriteString("search:\n  sources:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

// --- groupSearchResults: pure algorithm tests, fixed inputs, exact group
// order/per-group result order asserted per this packet's own Validation
// section. ---

func TestGroupSearchResults_OrderedByRegistrationPreservesPerSourceOrder(t *testing.T) {
	perSource := map[string][]schema.SearchResult{
		"backend-b": {{Type: "issue", ID: "5", Title: "only issue"}},
		"backend-a": {
			{Type: "pr", ID: "2", Title: "second-listed but first in source order"},
			{Type: "pr", ID: "1", Title: "first-listed but second in source order"},
		},
	}
	got := groupSearchResults(perSource, []string{"backend-a", "backend-b"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	// Groups ordered by search.sources registration order (backend-a
	// before backend-b), never re-sorted or interleaved.
	if got[0].Source != "backend-a" || got[1].Source != "backend-b" {
		t.Fatalf("group order = [%s %s], want [backend-a backend-b]", got[0].Source, got[1].Source)
	}
	// Each group's own Results slice preserves exactly that source's own
	// returned order — never re-sorted.
	if len(got[0].Results) != 2 || got[0].Results[0].ID != "2" || got[0].Results[1].ID != "1" {
		t.Fatalf("backend-a's group results = %+v, want [ID=2 ID=1] in that exact order", got[0].Results)
	}
	if len(got[1].Results) != 1 || got[1].Results[0].ID != "5" {
		t.Fatalf("backend-b's group results = %+v, want [ID=5]", got[1].Results)
	}
}

func TestGroupSearchResults_SkipsSourceNotSuccessfullyQueried(t *testing.T) {
	// backend-b is registered (in sourceOrder) but has no entry in
	// perSource — a disabled/degraded source contributes no group at all,
	// rather than an empty placeholder one; its own health already lives
	// in FanOutOutcome.Sources.
	perSource := map[string][]schema.SearchResult{
		"backend-a": {{Type: "pr", ID: "1", Title: "t1"}},
	}
	got := groupSearchResults(perSource, []string{"backend-a", "backend-b"})
	if len(got) != 1 || got[0].Source != "backend-a" {
		t.Fatalf("got = %+v, want exactly one group for backend-a", got)
	}
}

func TestGroupSearchResults_NeverMergesAcrossSources(t *testing.T) {
	// Two sources report what a human might consider "the same" result
	// (same type/id) — unlike attention, search performs no cross-source
	// dedup/merge: both must appear, each in its own group.
	perSource := map[string][]schema.SearchResult{
		"backend-a": {{Type: "pr", ID: "1", Title: "from a"}},
		"backend-b": {{Type: "pr", ID: "1", Title: "from b"}},
	}
	got := groupSearchResults(perSource, []string{"backend-a", "backend-b"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (no merge across sources): %+v", len(got), got)
	}
	if got[0].Results[0].Title != "from a" || got[1].Results[0].Title != "from b" {
		t.Fatalf("got = %+v, want each source's own contribution reported independently", got)
	}
}

func TestGroupSearchResults_EmptyWhenNoSourcesSucceeded(t *testing.T) {
	got := groupSearchResults(map[string][]schema.SearchResult{}, nil)
	if len(got) != 0 {
		t.Fatalf("got = %+v, want empty", got)
	}
}

// --- fanOutSearch: sources[] reporting, mirroring
// TestFanOutAttentionList_*'s own fixture style. ---

func TestFanOutSearch_Succeeded(t *testing.T) {
	writeFakeBackend(t, "backend-ok", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","title":"t1","url":"http://x/1","source":"backend-ok"}]}`)
	perSource, out := fanOutSearch(context.Background(), []string{"backend-ok"}, "query", nil)
	if len(out.Sources) != 1 || out.Sources[0].Status != SourceSucceeded || out.Sources[0].Count != 1 {
		t.Fatalf("sources = %+v", out.Sources)
	}
	if len(perSource["backend-ok"]) != 1 {
		t.Fatalf("perSource[backend-ok] = %+v, want 1 raw result", perSource["backend-ok"])
	}
}

func TestFanOutSearch_Degraded(t *testing.T) {
	writeFakeBackend(t, "backend-broken", `{"protocolVersion":1,"error":{"code":"unavailable","message":"boom"}}`)
	_, out := fanOutSearch(context.Background(), []string{"backend-broken"}, "query", nil)
	if len(out.Sources) != 1 || out.Sources[0].Status != SourceDegraded {
		t.Fatalf("sources = %+v", out.Sources)
	}
}

func TestFanOutSearch_DisabledNotApplicable(t *testing.T) {
	writeFakeBackend(t, "backend-nosearch", `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op \"search\""}}`)
	_, out := fanOutSearch(context.Background(), []string{"backend-nosearch"}, "query", nil)
	if len(out.Sources) != 1 {
		t.Fatalf("sources = %+v", out.Sources)
	}
	got := out.Sources[0]
	if got.Status != SourceDisabled || got.Reason != "not applicable" {
		t.Fatalf("source = %+v, want disabled/not applicable", got)
	}
}

func TestFanOutSearch_CountIsRawResultLengthNeverZeroPlaceholder(t *testing.T) {
	writeFakeBackend(t, "backend-ok", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","title":"t1","url":"http://x/1","source":"backend-ok"},{"type":"pr","id":"2","title":"t2","url":"http://x/2","source":"backend-ok"}]}`)
	_, out := fanOutSearch(context.Background(), []string{"backend-ok"}, "query", nil)
	if len(out.Sources) != 1 || out.Sources[0].Count != 2 {
		t.Fatalf("sources = %+v, want Count=2 (this source's own raw result count)", out.Sources)
	}
}

// --- validateSearchFields: field-validation unit tests per this packet's
// own Validation section. ---

func TestValidateSearchFields_CoreFieldNoWarning(t *testing.T) {
	got := validateSearchFields(context.Background(), nil, []string{"title"})
	if len(got) != 0 {
		t.Fatalf("warnings = %v, want none for a core-set field", got)
	}
}

func TestValidateSearchFields_BackendVocabularyFieldNoWarning(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-a", map[string]string{
		"capabilities": `{"protocolVersion":1,"schemaVersions":{"search":1},"ops":["search","capabilities"],"vocabulary":{"search_attributes":["custom_attr"]}}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)

	got := validateSearchFields(context.Background(), []string{"backend-a"}, []string{"custom_attr"})
	if len(got) != 0 {
		t.Fatalf("warnings = %v, want none for a field declared in a queried backend's own vocabulary", got)
	}
}

func TestValidateSearchFields_UnrecognizedFieldProducesExactlyOneWarning(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-a", map[string]string{
		"capabilities": `{"protocolVersion":1,"schemaVersions":{"search":1},"ops":["search","capabilities"],"vocabulary":{"search_attributes":["custom_attr"]}}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)

	got := validateSearchFields(context.Background(), []string{"backend-a"}, []string{"title", "custom_attr", "totally_unknown"})
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want exactly one (only totally_unknown is unrecognized)", got)
	}
	if !strings.Contains(got[0], "totally_unknown") {
		t.Fatalf("warnings[0] = %q, want it to name the unrecognized field", got[0])
	}
}

func TestValidateSearchFields_EmptyFieldsSkipsValidation(t *testing.T) {
	got := validateSearchFields(context.Background(), nil, nil)
	if got != nil {
		t.Fatalf("warnings = %v, want nil (an empty --fields skips validation entirely)", got)
	}
}

// --- CLI-level: "search <query>" end-to-end, mirroring auth_test.go's/
// attention_test.go's own fixture style. ---

func TestRun_Search_GroupsBySourceNeverInterleavedAndReportsSources(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","title":"t1","url":"http://x/1","source":"backend-a"}]}`)
	writeFakeBackend(t, "backend-b", `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op \"search\""}}`)
	writeSearchConfigFor(t, "backend-a", "backend-b")

	stdout, _, code := executePr(t, []string{"search", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"source":"backend-a"`) || !strings.Contains(stdout, `"id":"1"`) {
		t.Fatalf("stdout missing backend-a's own group: %s", stdout)
	}
	if !strings.Contains(stdout, `"status":"disabled"`) {
		t.Fatalf("stdout missing disabled sources[] row for backend-b: %s", stdout)
	}
}

func TestRun_Search_UnknownFieldWarnsNeverErrors(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","title":"t1","url":"http://x/1","source":"backend-a"}]}`)
	writeSearchConfigFor(t, "backend-a")

	stdout, _, code := executePr(t, []string{"search", "hello", "--fields", "totally_unknown"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (an unrecognized field is a warning, never an error): stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "totally_unknown") {
		t.Fatalf("stdout missing a warning naming the unrecognized field: %s", stdout)
	}
}

func TestRun_Search_NoResultFieldCarriesNoScore(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","title":"t1","url":"http://x/1","source":"backend-a"}]}`)
	writeSearchConfigFor(t, "backend-a")

	stdout, _, code := executePr(t, []string{"search", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, `"score"`) {
		t.Fatalf("stdout carries a score field, which schema.SearchResult MUST NOT have: %s", stdout)
	}
}
