package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/schemas"
)

func schemaDoc(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("schemas", name+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// strictDecode decodes data into v rejecting unknown fields.
func strictDecode(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("strict decode of %s: %v", data, err)
	}
}

func seq(n int64) *int64 { return &n }

// contractCase is one published type: a populated value, the schema it must
// satisfy, and a fresh value to decode a golden into.
type contractCase struct {
	name   string
	schema string
	value  any
	fresh  func() any
	golden string
}

func contractCases() []contractCase {
	return []contractCase{
		{
			name: "fan-out outcome", schema: "fanout-outcome",
			value:  FanOutOutcome{SchemaVersion: 1, Sources: []SourceResult{{Source: "a", Status: SourceSucceeded, Count: 2}, {Source: "b", Status: SourceDegraded, Reason: "rate limited"}}},
			fresh:  func() any { return new(FanOutOutcome) },
			golden: `{"schemaVersion":1,"sources":[{"source":"a","status":"succeeded","count":2},{"source":"b","status":"degraded","count":0,"reason":"rate limited"}]}`,
		},
		{
			name: "fan-out outcome without version", schema: "fanout-outcome",
			value:  FanOutOutcome{Sources: []SourceResult{{Source: "a", Status: SourceDisabled, Reason: "not applicable"}}},
			fresh:  func() any { return new(FanOutOutcome) },
			golden: `{"sources":[{"source":"a","status":"disabled","count":0,"reason":"not applicable"}]}`,
		},
		{
			name: "list outcome", schema: "list-outcome",
			value:  ListOutcome[json.RawMessage]{Entities: []json.RawMessage{json.RawMessage(`{"id":"X-1"}`)}, PresentIDs: []string{"X-1"}, Sources: []SourceResult{{Source: "a", Status: SourceSucceeded, Count: 1}}, Truncated: true, Fingerprints: map[string]string{"X-1": "abc"}},
			fresh:  func() any { return new(ListOutcome[json.RawMessage]) },
			golden: `{"entities":[{"id":"X-1"}],"present_ids":["X-1"],"sources":[{"source":"a","status":"succeeded","count":1}],"truncated":true,"fingerprints":{"X-1":"abc"}}`,
		},
		{
			name: "ci list outcome", schema: "ci-list-outcome",
			value:  CIListOutcome[json.RawMessage]{Runs: []json.RawMessage{json.RawMessage(`{"id":"r1"}`)}, Sources: []SourceResult{{Source: "gh", Status: SourceSucceeded, Count: 1}}},
			fresh:  func() any { return new(CIListOutcome[json.RawMessage]) },
			golden: `{"runs":[{"id":"r1"}],"sources":[{"source":"gh","status":"succeeded","count":1}]}`,
		},
		{
			name: "changes outcome", schema: "changes-outcome",
			value: ChangesOutcome{
				Sources:          []ChangesSource{{Backend: "gh", Status: SourceSucceeded, Version: 3, Truncated: false, NextSeq: seq(9), StoreIdentity: "s1"}},
				Changes:          []Change{{Change: ChangeAdded, Source: "gh", Entity: json.RawMessage(`{"id":"1"}`), Seq: seq(0), Kinds: json.RawMessage(`["new"]`), Fields: json.RawMessage(`{"a":1}`)}},
				SourcesFreshness: json.RawMessage(`[]`),
			},
			fresh:  func() any { return new(ChangesOutcome) },
			golden: `{"sources":[{"backend":"gh","status":"succeeded","version":3,"truncated":false,"next_seq":9,"store_identity":"s1"}],"changes":[{"change":"added","source":"gh","entity":{"id":"1"},"seq":0,"kinds":["new"],"fields":{"a":1}}],"sources_freshness":[]}`,
		},
		{
			name: "changes outcome minimal", schema: "changes-outcome",
			value:  ChangesOutcome{Sources: []ChangesSource{}, Changes: []Change{}},
			fresh:  func() any { return new(ChangesOutcome) },
			golden: `{"sources":[],"changes":[]}`,
		},
	}
}

// TestContractBothWays: every published type round-trips through the codec,
// its encoding validates against the checked-in schema, and its golden
// fixture decodes into the type with unknown fields rejected and re-encodes
// byte-identically.
func TestContractBothWays(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			doc := schemaDoc(t, tc.schema)

			enc, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := schemas.ValidateDocument(doc, enc); err != nil {
				t.Fatalf("encoding %s fails its schema: %v", enc, err)
			}
			back := tc.fresh()
			strictDecode(t, enc, back)
			again, err := json.Marshal(back)
			if err != nil || !bytes.Equal(enc, again) {
				t.Fatalf("round trip changed the encoding: %s vs %s (%v)", enc, again, err)
			}

			if err := schemas.ValidateDocument(doc, []byte(tc.golden)); err != nil {
				t.Fatalf("golden fails its schema: %v", err)
			}
			g := tc.fresh()
			strictDecode(t, []byte(tc.golden), g)
			regen, err := json.Marshal(g)
			if err != nil || !bytes.Equal(regen, []byte(tc.golden)) {
				t.Fatalf("golden does not re-encode identically:\n got %s\nwant %s (%v)", regen, tc.golden, err)
			}
		})
	}
}

// TestSchemaRejects pins that the checked-in schemas are strict where the
// contract is.
func TestSchemaRejects(t *testing.T) {
	for _, tc := range []struct{ schema, payload string }{
		{"fanout-outcome", `{"sources":[],"extra":1}`},
		{"fanout-outcome", `{"schemaVersion":"1","sources":[]}`},
		{"fanout-outcome", `{"sources":[{"source":"a","status":"weird","count":0}]}`},
		{"fanout-outcome", `{"schemaVersion":1}`},
		{"changes-outcome", `{"sources":[],"changes":[{"change":"moved","source":"a","entity":{}}]}`},
		{"list-outcome", `{"entities":[],"sources":[]}`},
	} {
		if err := schemas.ValidateDocument(schemaDoc(t, tc.schema), []byte(tc.payload)); err == nil {
			t.Errorf("%s accepted %s", tc.schema, tc.payload)
		}
	}
}
