package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTickItemPrintsOneTrivialItem(t *testing.T) {
	orig := tickItemNow
	tickItemNow = func() string { return "2026-09-22T00:00:00Z" }
	defer func() { tickItemNow = orig }()

	var out, errOut bytes.Buffer
	code := run([]string{"tick-item"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, errOut.String())
	}
	var items []tickItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v (stdout=%q)", err, out.String())
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 item, got %d", len(items))
	}
	item := items[0]
	if item.ID != "2026-09-22T00:00:00Z" {
		t.Fatalf("got id %q", item.ID)
	}
	if item.Type != "pg-router-probe-tick" {
		t.Fatalf("got type %q", item.Type)
	}
	if len(item.Metadata) != 0 {
		t.Fatalf("expected tick-item to carry no metadata, got %v", item.Metadata)
	}
}

func TestTickItemRejectsArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"tick-item", "unexpected"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 (usage error) for an unexpected arg, got %d", code)
	}
}
