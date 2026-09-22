package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTickItemEmitsOneTrivialItem(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"tick-item"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, errOut.String())
	}
	var items []tickItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("decode tick-item output: %v (stdout=%q)", err, out.String())
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Type != "ccpool-probe-tick" {
		t.Fatalf("type = %q", items[0].Type)
	}
}

func TestTickItemNoArgsAllowed(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"tick-item", "unexpected-arg"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 (usage error) for an unexpected arg, got %d", code)
	}
}
