package classify

import (
	"encoding/json"
	"strings"
	"testing"
)

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
