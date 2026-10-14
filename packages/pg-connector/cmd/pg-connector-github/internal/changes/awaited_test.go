package changes

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestDecodeAwaitedReadsEmptyNullAndEncodedForms(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("null"), json.RawMessage("[]")} {
		aw, err := DecodeAwaited(raw)
		if err != nil || len(aw) != 0 {
			t.Errorf("DecodeAwaited(%q) = %v, %v; want none", raw, aw, err)
		}
	}
	in := []Awaiting{{Group: "runs", Since: t0}, {Group: "detail", Since: t0.Add(time.Second)}}
	raw, err := encodeAwaited(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeAwaited(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Group != "runs" || !out[0].Since.Equal(t0) || !out[1].Since.Equal(t0.Add(time.Second)) {
		t.Errorf("round trip = %+v", out)
	}
	if _, err := DecodeAwaited(json.RawMessage(`{"x":1}`)); err == nil {
		t.Error("a document that is not a list was accepted")
	}
	raw, err = encodeAwaited(nil)
	if err != nil || string(raw) != "[]" {
		t.Errorf("encodeAwaited(nil) = %q, %v; want []", raw, err)
	}
}

func TestAwaitAddsRenewsAndOrdersByGroupTable(t *testing.T) {
	later := t0.Add(time.Minute)
	got := await([]Awaiting{{Group: "runs", Since: t0}, {Group: "files", Since: t0}}, []string{"detail", "files"}, later)
	var names []string
	for _, a := range got {
		names = append(names, a.Group)
	}
	if !slices.Equal(names, []string{"detail", "files", "runs"}) {
		t.Fatalf("order = %v, want detail files runs", names)
	}
	want := map[string]time.Time{"detail": later, "files": later, "runs": t0}
	for _, a := range got {
		if !a.Since.Equal(want[a.Group]) {
			t.Errorf("%s Since = %v, want %v", a.Group, a.Since, want[a.Group])
		}
	}
}
