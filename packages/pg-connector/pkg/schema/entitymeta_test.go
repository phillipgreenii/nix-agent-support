package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// EntityMeta is metadata beside an entity (bead pg2-0o693): additive on the
// wire, and an answer without it reads as "not stated", never as false.

func TestEntityMeta_WireShapeAndOmission(t *testing.T) {
	bare, err := json.Marshal(Issue{ID: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "entity_meta") {
		t.Errorf("an issue with no metadata must omit entity_meta, got %s", bare)
	}
	for _, tc := range []struct {
		watched bool
		want    string
	}{
		{true, `"entity_meta":{"watched":true}`},
		{false, `"entity_meta":{"watched":false}`}, // false is a statement, not an omission
	} {
		raw, err := json.Marshal(Issue{ID: "i", EntityMeta: WatchedMeta(tc.watched)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Errorf("watched=%v: %s lacks %s", tc.watched, raw, tc.want)
		}
	}
	// The key sits beside the content, not in it: the stored shape of an entity
	// that carries no metadata stays byte-identical to the shape without the field.
	if want := `{"id":"i","title":"","state":"","url":"","as_of":"","stale":false}`; string(bare) != want {
		t.Errorf("bare issue = %s, want %s", bare, want)
	}
}

func TestEntityMeta_AnAnswerWithoutTheFieldIsNotStated(t *testing.T) {
	for name, doc := range map[string]string{
		"an older daemon, no entity_meta":      `{"id":"i","title":"t","state":"s","url":"u","as_of":"a","stale":false}`,
		"entity_meta present, watched omitted": `{"id":"i","title":"t","state":"s","url":"u","as_of":"a","stale":false,"entity_meta":{}}`,
		"entity_meta null":                     `{"id":"i","title":"t","state":"s","url":"u","as_of":"a","stale":false,"entity_meta":null}`,
	} {
		var iss Issue
		if err := json.Unmarshal([]byte(doc), &iss); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if watched, stated := iss.EntityMeta.WatchedState(); stated || watched {
			t.Errorf("%s: WatchedState = (%v, %v), want (false, not stated)", name, watched, stated)
		}
	}
	var iss Issue
	if err := json.Unmarshal([]byte(`{"id":"i","entity_meta":{"watched":false,"future_key":1}}`), &iss); err != nil {
		t.Fatal(err)
	}
	if watched, stated := iss.EntityMeta.WatchedState(); !stated || watched {
		t.Errorf("an explicit false: WatchedState = (%v, %v), want (false, stated)", watched, stated)
	}
	if watched, stated := WatchedMeta(true).WatchedState(); !stated || !watched {
		t.Errorf("WatchedMeta(true).WatchedState = (%v, %v)", watched, stated)
	}
}
