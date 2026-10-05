package changes

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestEnvelopeMatchesGoldenV1 pins the exact shape of contract
// pg-desk.changes/v1 [design 9.3] in both directions: the golden decodes
// into Envelope without loss, and Envelope re-encodes to the golden bytes.
func TestEnvelopeMatchesGoldenV1(t *testing.T) {
	golden, err := os.ReadFile("testdata/envelope_v1.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	want := Envelope{
		Contract: Contract,
		Type:     "pr",
		Consumer: "pg-router",
		Cursor:   Cursor{From: 18400, To: 18422},
		Sources: []Source{
			{Query: "mine", Status: StatusOK},
			{Query: "team", Status: StatusDegraded, Reason: "rate_limited"},
		},
		Records: []Record{{
			Seq: 18422, Type: "pr", ID: "acme/api#123", Title: "Add retry to client",
			Version: 37, Kinds: []string{"head_changed", "feedback_changed"},
			Origin: "pg-connector", At: "2026-09-29T14:03:11Z",
		}},
	}
	// Compare compacted bytes: the repo formatter owns the golden file's
	// whitespace layout, the contract owns key names, order and values.
	got, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var compactGolden bytes.Buffer
	if err := json.Compact(&compactGolden, golden); err != nil {
		t.Fatalf("compact golden: %v", err)
	}
	if !bytes.Equal(got, compactGolden.Bytes()) {
		t.Errorf("envelope encoding drifted from golden:\n got: %s\nwant: %s", got, compactGolden.Bytes())
	}

	dec := json.NewDecoder(bytes.NewReader(golden))
	dec.DisallowUnknownFields()
	var round Envelope
	if err := dec.Decode(&round); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	again, _ := json.Marshal(round)
	if !bytes.Equal(again, got) {
		t.Errorf("golden did not round-trip:\n%s", again)
	}
}

func TestContractAndStatusConstants(t *testing.T) {
	if Contract != "pg-desk.changes/v1" {
		t.Errorf("Contract = %q", Contract)
	}
	if StatusOK != "ok" || StatusDegraded != "degraded" || StatusFailed != "failed" {
		t.Errorf("status constants drifted: %q %q %q", StatusOK, StatusDegraded, StatusFailed)
	}
}

// TestSourceReasonOmittedWhenEmpty pins that an ok source carries no reason key.
func TestSourceReasonOmittedWhenEmpty(t *testing.T) {
	b, _ := json.Marshal(Source{Query: "mine", Status: StatusOK})
	if string(b) != `{"query":"mine","status":"ok"}` {
		t.Errorf("source = %s", b)
	}
}
