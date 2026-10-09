package store

import (
	"bytes"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// TestScanBatchIdentityIgnoresIdOrder pins that a batch is matched by equal
// ids only: an intruding batch or a batch.committed of another batch is
// corruption whether its id sorts before or after the open batch's id.
func TestScanBatchIdentityIgnoresIdOrder(t *testing.T) {
	low, high := batchID(1), batchID(2)
	if low >= high {
		t.Fatalf("batchID(1) %s does not sort before batchID(2) %s", low, high)
	}
	m := func(n int, b event.ID) []byte { return enc(t, memberEvent(n, b)) }
	c := func(n int, b event.ID) []byte { return enc(t, commitEvent(n, b)) }
	cases := []struct {
		name string
		log  []byte
		line int
	}{
		{"a member of a lower batch inside a higher one", join(m(1, high), m(2, low), c(3, low)), 2},
		{"a member of a higher batch inside a lower one", join(m(1, low), m(2, high), c(3, high)), 2},
		{"a lower batch.committed closing a higher batch", join(m(1, high), c(2, low)), 2},
		{"a higher batch.committed closing a lower batch", join(m(1, low), c(2, high)), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := scan(bytes.NewReader(tc.log))
			_ = wantCorrupt(t, err, tc.line)
		})
	}
}
