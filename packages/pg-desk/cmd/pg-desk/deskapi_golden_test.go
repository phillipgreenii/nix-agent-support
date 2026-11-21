package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestProducerGoldensAreTheContractGoldens keeps the producer's own golden
// files equal to the published contract's golden files, which the consumers'
// decoder tests read in place.
func TestProducerGoldensAreTheContractGoldens(t *testing.T) {
	for local, contract := range map[string]string{
		"testdata/show_pr.golden.json":                "../../pkg/deskapi/testdata/golden/view_pr.json",
		"testdata/show_issue.golden.json":             "../../pkg/deskapi/testdata/golden/view_issue.json",
		"testdata/show_thread.golden.json":            "../../pkg/deskapi/testdata/golden/view_thread.json",
		"testdata/changes_issue_envelope.golden.json": "../../pkg/deskapi/testdata/golden/changes_issue.json",
	} {
		a, err := os.ReadFile(local)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(contract)
		if err != nil {
			t.Fatal(err)
		}
		var ca, cb bytes.Buffer
		if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
			t.Fatalf("%s: golden is not JSON", local)
		}
		if ca.String() != cb.String() {
			t.Errorf("%s differs from %s", local, contract)
		}
	}
}
