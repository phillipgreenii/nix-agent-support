package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// Bead pg2-wyeq4 (operator ruling, Phillip, 2026-10-09, reversing D3 of
// pg2-m482k for the beads backend only): this backend answers search (a
// wrapped `bd search`) and a NEW label-driven list_attention again. The tests
// below pin the wiring, the advertised capabilities and the wire behavior
// through the generic table backend, with a fake bd and no live dependence.

const attentionSearchBdJSON = `{"data":[
	{"id":"tp-2","title":"second","status":"open","priority":2,"issue_type":"task","labels":["attention"],"updated_at":"2026-10-02T00:00:00Z"},
	{"id":"tp-1","title":"first","status":"in_progress","priority":0,"issue_type":"bug","labels":["human-focus","x"],"updated_at":"2026-10-01T00:00:00Z"},
	{"id":"tp-3","title":"parked","status":"deferred","priority":0,"issue_type":"task","labels":["attention"],"updated_at":"2026-10-03T00:00:00Z"},
	{"id":"tp-4","title":"unrelated","status":"open","priority":1,"issue_type":"task","labels":["other"],"updated_at":"2026-10-04T00:00:00Z"}
],"schema_version":1}`

func attentionSearchBackend(config string) conformance.Backend {
	runner := &fakeRunner{handle: func([]string) (string, error) { return attentionSearchBdJSON, nil }}
	inner := conformance.TableBackend{Table: newDispatchTable(internal.New(runner))}
	if config == "" {
		return inner
	}
	return configInjectingBackend{inner: inner, config: config}
}

func invokeRaw(t *testing.T, b conformance.Backend, request string) scriptout.Response {
	t.Helper()
	out, _, err := b.Invoke(context.Background(), []byte(request))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return resp
}

func TestNewDispatchTable_RegistersSearchAndListAttentionWithoutAuthStatus(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	for _, op := range []string{"search", "list_attention"} {
		if _, ok := table[op]; !ok {
			t.Errorf("dispatch table has no %q entry", op)
		}
	}
	if _, ok := table[scriptout.OpAuthStatus]; ok {
		t.Error("dispatch table gained auth_status; Backend is not a provider.AuthChecker")
	}
	if got := table["list_attention"].SchemaVersion; got != schema.AttentionSchemaVersion {
		t.Errorf("list_attention schema version = %d, want %d", got, schema.AttentionSchemaVersion)
	}
	if got := table["search"].SchemaVersion; got != schema.SearchSchemaVersion {
		t.Errorf("search schema version = %d, want %d", got, schema.SearchSchemaVersion)
	}
}

func TestCapabilities_AdvertiseSearchAndAttention(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	result, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("capabilities Handle: %v", err)
	}
	resp := result.(scriptout.CapabilitiesResponse)
	for _, op := range []string{"search", "list_attention", "list_activity", "show"} {
		if !slices.Contains(resp.Ops, op) {
			t.Errorf("capabilities.ops = %v, want %q", resp.Ops, op)
		}
	}
	if slices.Contains(resp.Ops, scriptout.OpAuthStatus) {
		t.Errorf("capabilities.ops = %v, must not claim auth_status", resp.Ops)
	}
	if got := resp.SchemaVersions["attention"]; got != schema.AttentionSchemaVersion {
		t.Errorf("schemaVersions[attention] = %d, want %d", got, schema.AttentionSchemaVersion)
	}
	if got := resp.SchemaVersions["search"]; got != schema.SearchSchemaVersion {
		t.Errorf("schemaVersions[search] = %d, want %d", got, schema.SearchSchemaVersion)
	}
	// The umbrella's --fields probe reads vocabulary.search_attributes (see
	// cmd/pg-connector/search.go searchAttributesVocabularyKey): declaring
	// the names is what keeps a requested one from drawing a warning.
	raw, _ := json.Marshal(resp.Vocabulary["search_attributes"])
	want, _ := json.Marshal(internal.SearchAttributes)
	if string(raw) != string(want) || len(internal.SearchAttributes) == 0 {
		t.Errorf("vocabulary.search_attributes = %s, want %s (non-empty)", raw, want)
	}
}

func TestListAttention_WireReturnsLabelScopedItemsInPriorityOrder(t *testing.T) {
	b := attentionSearchBackend(`{"attention_labels":["attention","human-focus"]}`)
	resp := invokeRaw(t, b, `{"op":"list_attention","args":{}}`)
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	var items []schema.AttentionItem
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	// tp-3 is deferred (dropped), tp-4 has no configured label (dropped).
	if got := strings.Join(ids, ","); got != "tp-1,tp-2" {
		t.Fatalf("ids = %s, want tp-1,tp-2", got)
	}
	if items[0].Severity != schema.SeverityCritical || items[1].Severity != schema.SeverityMedium {
		t.Errorf("severities = %q,%q, want critical,medium", items[0].Severity, items[1].Severity)
	}
	if resp.SchemaVersion != schema.AttentionSchemaVersion {
		t.Errorf("response schemaVersion = %d, want %d", resp.SchemaVersion, schema.AttentionSchemaVersion)
	}
}

func TestListAttention_WireWithoutLabelsIsUnavailableNamingTheKey(t *testing.T) {
	for _, config := range []string{"", `{"attention_labels":[]}`} {
		resp := invokeRaw(t, attentionSearchBackend(config), `{"op":"list_attention","args":{}}`)
		if resp.Error == nil || resp.Error.Code != "unavailable" {
			t.Fatalf("config %q: response = %+v, want error unavailable", config, resp)
		}
		if !strings.Contains(resp.Error.Message, "attention_labels") {
			t.Errorf("config %q: message = %q, want it to name attention_labels", config, resp.Error.Message)
		}
	}
}

func TestSearch_WireReturnsHitsWithRequestedAttributes(t *testing.T) {
	b := attentionSearchBackend("")
	resp := invokeRaw(t, b, `{"op":"search","args":{"query":"second","fields":["status","bogus"]}}`)
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	var results []schema.SearchResult
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want the 4 hits the fake bd returns (search does not re-filter)", len(results))
	}
	first := results[0]
	if first.Type != "issue" || first.ID != "tp-2" || first.Title != "second" || first.Source != "pg-connector-issue-beads" {
		t.Errorf("first = %+v", first)
	}
	if want := (map[string]any{"status": "open"}); !reflect.DeepEqual(first.Attributes, want) {
		t.Errorf("attributes = %#v, want %#v", first.Attributes, want)
	}
	if resp.SchemaVersion != schema.SearchSchemaVersion {
		t.Errorf("response schemaVersion = %d, want %d", resp.SchemaVersion, schema.SearchSchemaVersion)
	}
}

func TestSearch_WireEmptyQueryIsInvalidArgument(t *testing.T) {
	resp := invokeRaw(t, attentionSearchBackend(""), `{"op":"search","args":{"query":"  "}}`)
	if resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Fatalf("response = %+v, want error invalid_argument", resp)
	}
}

func TestSearch_WireHonoursSearchTimeBoundFromConfig(t *testing.T) {
	b := attentionSearchBackend(`{"search_since":"2026-10-02T00:00:00Z","search_before":"2026-10-04T00:00:00Z"}`)
	resp := invokeRaw(t, b, `{"op":"search","args":{"query":"x"}}`)
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	var results []schema.SearchResult
	raw, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(raw, &results)
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "tp-2,tp-3" {
		t.Errorf("ids = %s, want tp-2,tp-3 (updated_at in [since, before))", got)
	}
}

func TestConformance_GenericSuiteStillPasses(t *testing.T) {
	for _, r := range conformance.Run(context.Background(), attentionSearchBackend("")) {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}
