package workitem

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// updateGolden regenerates share/work-item-keys.json from the contract table:
//
//	go test ./internal/workitem/ -run KeyList -update
var updateGolden = flag.Bool("update", false, "regenerate share/work-item-keys.json from the contract table")

const goldenPath = "../../share/work-item-keys.json"

func TestWorkItemKeyListMatchesGolden(t *testing.T) {
	want, err := MarshalKeyList()
	if err != nil {
		t.Fatalf("MarshalKeyList: %v", err)
	}
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with: go test ./internal/workitem/ -run KeyList -update)", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the work-item contract table.\n"+
			"Regenerate it with: go test ./internal/workitem/ -run KeyList -update\n"+
			"and note that Phase 9's deployment-repo check consumes this file "+
			"(installed at $out/share/pg-decider/work-item-keys.json).\n--- committed ---\n%s\n--- derived ---\n%s",
			goldenPath, got, want)
	}
}

func TestKeyListShape(t *testing.T) {
	kl := KeyList()
	if kl.Contract != KeyListContract {
		t.Errorf("Contract = %q, want %q", kl.Contract, KeyListContract)
	}
	if KeyListContract != "pg-decider.work-item-keys/v1" {
		t.Errorf("KeyListContract = %q", KeyListContract)
	}
	if len(kl.Kinds) != len(Kinds()) {
		t.Fatalf("got %d kinds, want %d", len(kl.Kinds), len(Kinds()))
	}
	for _, k := range Kinds() {
		keys, ok := kl.Kinds[string(k)]
		if !ok {
			t.Fatalf("kind %q missing", k)
		}
		if !sort.StringsAreSorted(keys) {
			t.Errorf("kind %q keys not sorted: %v", k, keys)
		}
		seen := map[string]bool{}
		for _, key := range keys {
			if seen[key] {
				t.Errorf("kind %q duplicate key %q", k, key)
			}
			seen[key] = true
		}
		c := ContractFor(k)
		for _, mk := range c.MetadataKeys {
			if !seen[mk] {
				t.Errorf("kind %q missing metadata key %q", k, mk)
			}
		}
		for _, f := range []string{"issue_type", "title"} {
			if !seen[f] {
				t.Errorf("kind %q missing %q", k, f)
			}
		}
		if seen["labels"] != (len(c.Labels) > 0) {
			t.Errorf("kind %q labels presence = %v, contract labels = %v", k, seen["labels"], c.Labels)
		}
		if seen["parent"] != (c.Parent != "") {
			t.Errorf("kind %q parent presence = %v, contract parent = %q", k, seen["parent"], c.Parent)
		}
		if want := 2 + len(c.MetadataKeys) + b2i(len(c.Labels) > 0) + b2i(c.Parent != ""); len(keys) != want {
			t.Errorf("kind %q has %d keys, want %d: %v", k, len(keys), want, keys)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
