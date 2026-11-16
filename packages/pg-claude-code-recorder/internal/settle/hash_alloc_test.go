package settle

import (
	"bytes"
	"testing"
)

// hashReader reuses its read buffer: repeated calls must not allocate a fresh
// 1 MiB buffer each time.
func TestHashReaderReusesItsBuffer(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 3<<20)
	r := bytes.NewReader(data)
	if _, err := hashReader(r, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := hashReader(r, int64(len(data))); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("allocs per hashReader call: %.0f", allocs)
	if allocs > 4 {
		t.Fatalf("hashReader allocated %.0f objects per call, want at most 4", allocs)
	}
}
