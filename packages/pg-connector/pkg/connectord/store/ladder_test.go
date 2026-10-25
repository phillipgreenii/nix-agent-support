package store

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

// shippedSchemaHashes are the sha256 of the SQL text of the shipped rungs, taken
// from the code as it stood when rung 4 landed. A shipped rung MUST NOT change:
// a store that already climbed it would differ from one that climbs it later.
var shippedSchemaHashes = map[string]string{
	"schemaV1": "d8fa5b3828bc16575f9f410e146e09a37f6822b75825bcda76ab2c4a82a0eb70",
	"schemaV2": "c8900f389fe5b0699de3c41e5384788b6407dedda71f3b9a9833c1b2c3f89fb5",
	"schemaV3": "68da871c61d34279a40cbef938f0ed2e5d2b7b88dce7a44bd526eb2b80ccf2a8",
}

func TestLadderPinsTheShippedSchemaTextOfRungs1To3(t *testing.T) {
	for name, text := range map[string]string{"schemaV1": schemaV1, "schemaV2": schemaV2, "schemaV3": schemaV3} {
		sum := sha256.Sum256([]byte(text))
		if got := hex.EncodeToString(sum[:]); got != shippedSchemaHashes[name] {
			t.Errorf("%s changed: sha256 %s, shipped %s (a shipped rung MUST NOT change; add a rung instead)",
				name, got, shippedSchemaHashes[name])
		}
	}
}

// TestLadderPinsTheRungNamesByIndex pins the first four rungs by index. A later
// rung appends and does not edit this list; the exact final list is the last
// rung's own test.
func TestLadderPinsTheRungNamesByIndex(t *testing.T) {
	want := []string{
		"query, alias, entity, entity_group, budget",
		"job",
		"pending_change, change_log, consumer",
		"store identity",
	}
	ladder := BuiltinMigrations()
	if len(ladder) < len(want) {
		t.Fatalf("the ladder has %d rungs, want at least %d", len(ladder), len(want))
	}
	var got []string
	for i := range want {
		got = append(got, ladder[i].Name)
		if ladder[i].Version != i+1 {
			t.Errorf("rung at index %d has version %d, want %d", i, ladder[i].Version, i+1)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("rung names = %q, want %q", got, want)
	}
}

// TestLadderFinalRungNamesAndOrder is the exact final list of the ladder, in
// order (rung 8, alias folded-ref index, is the last rung): a later consumer that needs
// a rung adds it with a new ladder step and edits this list with it.
func TestLadderFinalRungNamesAndOrder(t *testing.T) {
	want := []string{
		"query, alias, entity, entity_group, budget",
		"job",
		"pending_change, change_log, consumer",
		"store identity",
		"watched flag and log snapshot",
		"consumer cursor per consumer",
		"entity closed-at",
		"alias folded-ref index",
	}
	var got []string
	for i, m := range BuiltinMigrations() {
		got = append(got, m.Name)
		if m.Version != i+1 {
			t.Errorf("rung at index %d has version %d, want %d", i, m.Version, i+1)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("rung names = %q, want %q", got, want)
	}
}
