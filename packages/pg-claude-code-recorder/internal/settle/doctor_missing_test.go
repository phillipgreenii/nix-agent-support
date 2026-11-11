package settle

import (
	"bytes"
	"testing"
)

func TestMissingBytesRepairable(t *testing.T) {
	tests := []struct {
		name     string
		notFound bool
		mode     string
		want     bool
	}{
		{"not found snapshot", true, "snapshot", true},
		{"not found append is a plain mismatch", true, "append", false},
		{"not found with an empty mode", true, "", false},
		{"other error on a snapshot is a plain mismatch", false, "snapshot", false},
		{"other error on an append", false, "append", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingBytesRepairable(tc.notFound, tc.mode); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The data is five bytes: the smallest size where "one under" (4) and "one
// over" (6) are both non-empty and distinct from the real length, so a size
// check cannot be confused with a hash check. The empty case covers the zero
// length edge of the hash loop.
func TestSourceMatchesManifest(t *testing.T) {
	data := []byte("hello")
	h, _ := hashReader(bytes.NewReader(data), int64(len(data)))
	good := hexHash(h)
	eh, _ := hashReader(bytes.NewReader(nil), 0)
	tests := []struct {
		name string
		data []byte
		size int64
		hash string
		want bool
	}{
		{"matches", data, 5, good, true},
		{"size one under", data, 4, good, false},
		{"size one over", data, 6, good, false},
		{"same size, other bytes", []byte("hellp"), 5, good, false},
		{"wrong hash", data, 5, "0", false},
		{"empty source matches an empty manifest", nil, 0, hexHash(eh), true},
		{"empty source against a non-empty manifest", nil, 5, good, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceMatchesManifest(tc.data, tc.size, tc.hash); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
