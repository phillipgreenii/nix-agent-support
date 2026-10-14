package classify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFieldJSONNamesAndNullSides(t *testing.T) {
	f := Field{Name: "title", Before: raw(`"a"`)}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"title","before":"a","after":null}` {
		t.Fatalf("Field JSON = %s", b)
	}
	var back Field
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Before) != `"a"` || back.After != nil {
		t.Fatalf("round trip = %+v: a null side must be the empty RawMessage", back)
	}
}

func TestEncodeDecodeFields(t *testing.T) {
	if got, err := EncodeFields(nil); err != nil || string(got) != "[]" {
		t.Fatalf("EncodeFields(nil) = %s, %v; an empty list encodes as []", got, err)
	}
	in := []Field{
		{Name: "a", Before: raw(`1`), After: raw(`2`)},
		{Name: "b", After: raw(` {"x": 1} `)},
		{Name: "c", Before: raw(`"gone"`)},
	}
	enc, err := EncodeFields(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeFields(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Name != "a" || string(out[1].After) != `{"x":1}` || out[1].Before != nil || out[2].After != nil {
		t.Fatalf("decoded = %+v", out)
	}
	if out, err := DecodeFields(raw(`[]`)); err != nil || out == nil || len(out) != 0 {
		t.Fatalf("DecodeFields([]) = %v, %v", out, err)
	}
	for _, empty := range []json.RawMessage{nil, raw(``), raw(`null`)} {
		if out, err := DecodeFields(empty); err != nil || len(out) != 0 {
			t.Errorf("DecodeFields(%q) = %v, %v", empty, out, err)
		}
	}
	if _, err := DecodeFields(raw(`{"not":"a list"}`)); err == nil {
		t.Error("DecodeFields accepted an object")
	}
	if _, err := EncodeFields([]Field{{Name: "bad", After: raw(`{nope`)}}); err == nil {
		t.Error("EncodeFields accepted a value that is not JSON")
	}
}

func TestCapText(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
		want  string
	}{
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcde"},
		{"abc", 10, "abc"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		// "é" is two bytes: a cut inside it backs up to the character boundary.
		{"aéb", 2, "a"},
		{"aéb", 3, "aé"},
	} {
		if got := capText(tc.in, tc.limit); got != tc.want {
			t.Errorf("capText(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
		if len(capText(tc.in, tc.limit)) > max(tc.limit, 0) {
			t.Errorf("capText(%q, %d) exceeds the limit", tc.in, tc.limit)
		}
	}
}

func TestBodyValueCarriesCappedTextAndFullHash(t *testing.T) {
	long := strings.Repeat("x", 30)
	var v struct{ Body, Hash string }
	if err := json.Unmarshal(bodyValue(long, 10), &v); err != nil {
		t.Fatal(err)
	}
	if v.Body != strings.Repeat("x", 10) || v.Hash != textHash(long) || len(v.Hash) != 64 {
		t.Fatalf("body value = %+v", v)
	}
	// Two texts that share the capped prefix still differ by hash.
	if textHash(long) == textHash(long+"y") {
		t.Fatal("hash ignores the tail")
	}
}
