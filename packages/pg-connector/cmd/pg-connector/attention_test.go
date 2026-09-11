package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// writeAttentionConfigFor writes a top-level attention.sources registry
// listing backends (in order) and points $PG_PR_CONFIG at it.
// attention.sources is always list-valued and independent of
// connector.<type> (registry.go), mirroring writeCiConfigFor's own
// convention for connector.ci.
func writeAttentionConfigFor(t *testing.T, backends ...string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	var sb strings.Builder
	sb.WriteString("attention:\n  sources:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

// --- mergeAttentionItems: pure algorithm tests, fixed inputs, exact
// output order/via/truncated/total_before_cap asserted per this packet's
// own Validation section. ---

func TestMergeAttentionItems_NoOverlap(t *testing.T) {
	perSource := map[string][]schema.AttentionItem{
		"backend-a": {{Type: "pr", ID: "1", Summary: "needs review", Severity: schema.SeverityHigh}},
		"backend-b": {{Type: "issue", ID: "2", Summary: "stale", Severity: schema.SeverityLow}},
	}
	got := mergeAttentionItems(perSource, []string{"backend-a", "backend-b"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	// severityRank descending: high (backend-a's item) before low
	// (backend-b's item) — no dedup since {type,id} differ.
	if got[0].Type != "pr" || got[0].ID != "1" || len(got[0].Via) != 1 || got[0].Via[0] != "backend-a" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].Type != "issue" || got[1].ID != "2" || len(got[1].Via) != 1 || got[1].Via[0] != "backend-b" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestMergeAttentionItems_DedupSeverityTieBrokenByConfigOrder(t *testing.T) {
	// Both sources report the same {type,id} at the same severity
	// (medium) — the design's own dedup rule says a tie at the same
	// severity is broken by attention.sources config order (earliest
	// wins), so backend-a's own summary must win even though it is
	// registered/queried second in this map's iteration (map order is
	// irrelevant; sourceOrder is what governs).
	perSource := map[string][]schema.AttentionItem{
		"backend-b": {{Type: "pr", ID: "1", Summary: "from b", Severity: schema.SeverityMedium}},
		"backend-a": {{Type: "pr", ID: "1", Summary: "from a", Severity: schema.SeverityMedium}},
	}
	got := mergeAttentionItems(perSource, []string{"backend-a", "backend-b"})
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (deduped): %+v", len(got), got)
	}
	item := got[0]
	if item.Summary != "from a" {
		t.Fatalf("Summary = %q, want %q (earliest-configured source wins the tie)", item.Summary, "from a")
	}
	if len(item.Via) != 2 || item.Via[0] != "backend-a" || item.Via[1] != "backend-b" {
		t.Fatalf("Via = %v, want [backend-a backend-b] (config order)", item.Via)
	}
}

func TestMergeAttentionItems_ViaLengthTiebreak(t *testing.T) {
	// item-1 is reported by two sources (backend-a, backend-b) at low
	// severity; item-2 is reported by one source (backend-c) at the SAME
	// (low) severity. Both are the same severityRank, so via.length
	// descending must place the two-source item first.
	perSource := map[string][]schema.AttentionItem{
		"backend-a": {{Type: "pr", ID: "1", Summary: "s1", Severity: schema.SeverityLow}},
		"backend-b": {{Type: "pr", ID: "1", Summary: "s1", Severity: schema.SeverityLow}},
		"backend-c": {{Type: "pr", ID: "2", Summary: "s2", Severity: schema.SeverityLow}},
	}
	got := mergeAttentionItems(perSource, []string{"backend-a", "backend-b", "backend-c"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != "1" || len(got[0].Via) != 2 {
		t.Fatalf("got[0] = %+v, want the two-source item first (via.length tiebreak)", got[0])
	}
	if got[1].ID != "2" || len(got[1].Via) != 1 {
		t.Fatalf("got[1] = %+v, want the one-source item second", got[1])
	}
}

func TestMergeAttentionItems_MissingSeveritySortsAsMediumButNotReportedAsMedium(t *testing.T) {
	// backend-a's item has no severity opinion at all (omitted). It must
	// sort as if medium (ahead of an explicit "low" item, behind an
	// explicit "high" one) but its own Severity field must stay empty on
	// the wire — never rewritten to "medium".
	perSource := map[string][]schema.AttentionItem{
		"backend-a": {{Type: "pr", ID: "1", Summary: "no opinion"}}, // Severity omitted.
		"backend-b": {{Type: "pr", ID: "2", Summary: "low", Severity: schema.SeverityLow}},
		"backend-c": {{Type: "pr", ID: "3", Summary: "high", Severity: schema.SeverityHigh}},
	}
	got := mergeAttentionItems(perSource, []string{"backend-a", "backend-b", "backend-c"})
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	if got[0].ID != "3" {
		t.Fatalf("got[0].ID = %q, want %q (high ranks first)", got[0].ID, "3")
	}
	if got[1].ID != "1" {
		t.Fatalf("got[1].ID = %q, want %q (missing severity ranks as medium, ahead of low)", got[1].ID, "1")
	}
	if got[1].Severity != "" {
		t.Fatalf("got[1].Severity = %q, want empty — missing severity must never be reported as medium", got[1].Severity)
	}
	if got[2].ID != "2" {
		t.Fatalf("got[2].ID = %q, want %q (low ranks last)", got[2].ID, "2")
	}
}

func TestMergeAttentionItems_PreservesEachSourcesOwnItemOrderOnFullTie(t *testing.T) {
	// Two items from the SAME source, same severity, no dedup overlap —
	// tied on every real sort key (severity, via.length==1, config
	// order==same source), so the final tiebreak is that source's own
	// original item order: item-2 (reported first by backend-a) must
	// stay before item-1 (reported second).
	perSource := map[string][]schema.AttentionItem{
		"backend-a": {
			{Type: "pr", ID: "2", Summary: "second-listed but first in source order", Severity: schema.SeverityLow},
			{Type: "pr", ID: "1", Summary: "first-listed but second in source order", Severity: schema.SeverityLow},
		},
	}
	got := mergeAttentionItems(perSource, []string{"backend-a"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != "2" || got[1].ID != "1" {
		t.Fatalf("order = [%s %s], want [2 1] (each source's own item order preserved)", got[0].ID, got[1].ID)
	}
}

// --- fanOutAttentionList: sources[] reporting, mirroring
// TestFanOutAuthStatus_*'s own fixture style. ---

func TestFanOutAttentionList_Succeeded(t *testing.T) {
	writeFakeBackend(t, "backend-ok", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","summary":"s"}]}`)
	perSource, out := fanOutAttentionList(context.Background(), nil, []string{"backend-ok"})
	if len(out.Sources) != 1 || out.Sources[0].Status != SourceSucceeded || out.Sources[0].Count != 1 {
		t.Fatalf("sources = %+v", out.Sources)
	}
	if len(perSource["backend-ok"]) != 1 {
		t.Fatalf("perSource[backend-ok] = %+v, want 1 raw item", perSource["backend-ok"])
	}
}

func TestFanOutAttentionList_Degraded(t *testing.T) {
	writeFakeBackend(t, "backend-broken", `{"protocolVersion":1,"error":{"code":"unavailable","message":"boom"}}`)
	_, out := fanOutAttentionList(context.Background(), nil, []string{"backend-broken"})
	if len(out.Sources) != 1 || out.Sources[0].Status != SourceDegraded {
		t.Fatalf("sources = %+v", out.Sources)
	}
}

func TestFanOutAttentionList_DisabledNotApplicable(t *testing.T) {
	writeFakeBackend(t, "backend-noattention", `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op \"list_attention\""}}`)
	_, out := fanOutAttentionList(context.Background(), nil, []string{"backend-noattention"})
	if len(out.Sources) != 1 {
		t.Fatalf("sources = %+v", out.Sources)
	}
	got := out.Sources[0]
	if got.Status != SourceDisabled || got.Reason != "not applicable" {
		t.Fatalf("source = %+v, want disabled/not applicable", got)
	}
}

// TestFanOutAttentionList_AttachesBackendConfig proves fanOutAttentionList
// (bead pg2-7wqkr) actually attaches a backend's own registered
// backends.<name> config block to the outgoing list_attention request --
// unlike fanOutSearch's own still-nil config, list_attention now needs
// this so a deadline-based backend's configured attention_threshold/
// attention_exclude (home/programs/pg-connector's attention.perBackend
// option) actually reaches it. The fake backend below only answers
// successfully when its own stdin request carries the expected
// attention_threshold value, mirroring writeOpAwareFakeBackend's
// grep-the-request style rather than writeEchoFakeBackend's verbatim
// echo (whose reply shape — the whole request object — cannot itself
// decode as []schema.AttentionItem).
func TestFanOutAttentionList_AttachesBackendConfig(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "backend-config-aware")
	content := "#!/bin/sh\n" +
		"req=$(cat)\n" +
		"case \"$req\" in\n" +
		"  *attention_threshold*72h*) printf '{\"protocolVersion\":1,\"schemaVersion\":1,\"result\":[{\"type\":\"pr\",\"id\":\"1\",\"summary\":\"s\"}]}\\n' ;;\n" +
		"  *) printf '{\"protocolVersion\":1,\"error\":{\"code\":\"unavailable\",\"message\":\"config missing\"}}\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	reg, err := parseRegistry([]byte(
		"attention:\n  sources:\n    - backend-config-aware\n"+
			"backends:\n  backend-config-aware:\n    attention_threshold: \"72h\"\n",
	), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	perSource, out := fanOutAttentionList(context.Background(), reg, []string{"backend-config-aware"})
	if len(out.Sources) != 1 || out.Sources[0].Status != SourceSucceeded {
		t.Fatalf("sources = %+v, want succeeded (backend must have received attention_threshold=72h in its config)", out.Sources)
	}
	if len(perSource["backend-config-aware"]) != 1 {
		t.Fatalf("perSource = %+v, want 1 item", perSource["backend-config-aware"])
	}
}

// --- CLI-level: "attention list" end-to-end, mirroring pr_test.go's
// executePr helper. ---

func TestRun_AttentionList_MergesAndReportsSources(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","summary":"needs review","severity":"high"}]}`)
	writeFakeBackend(t, "backend-b", `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op \"list_attention\""}}`)
	writeAttentionConfigFor(t, "backend-a", "backend-b")

	stdout, _, code := executePr(t, []string{"attention", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"id":"1"`) {
		t.Fatalf("stdout missing merged item: %s", stdout)
	}
	if !strings.Contains(stdout, `"source":"backend-b"`) || !strings.Contains(stdout, `"status":"disabled"`) {
		t.Fatalf("stdout missing disabled sources[] row for backend-b: %s", stdout)
	}
}

func TestRun_AttentionList_Cap_TruncatesAndMarksManifest(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","summary":"a","severity":"high"},{"type":"pr","id":"2","summary":"b","severity":"low"}]}`)
	writeAttentionConfigFor(t, "backend-a")

	stdout, _, code := executePr(t, []string{"attention", "list", "--cap", "1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"truncated":true`) {
		t.Fatalf("stdout missing truncated:true: %s", stdout)
	}
	if !strings.Contains(stdout, `"total_before_cap":2`) {
		t.Fatalf("stdout missing total_before_cap:2 (the merged count, not the cap value): %s", stdout)
	}
	if strings.Contains(stdout, `"id":"2"`) {
		t.Fatalf("stdout still contains the capped-away item: %s", stdout)
	}
}

func TestRun_AttentionList_NoCap_NoTruncationMarker(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":[{"type":"pr","id":"1","summary":"a","severity":"high"}]}`)
	writeAttentionConfigFor(t, "backend-a")

	stdout, _, code := executePr(t, []string{"attention", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "truncated") || strings.Contains(stdout, "total_before_cap") {
		t.Fatalf("stdout carries a truncation marker with no --cap passed: %s", stdout)
	}
}
