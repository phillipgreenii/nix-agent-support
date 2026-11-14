//go:build soak

package store

import "testing"

// Scale version of the first-collection check (V-4: load-shaped, so soak tag only;
// run by the pg-claude-code-recorder-soak check): 30,000 rows make an unbounded
// first ANALYZE hold the write lock long enough for the concurrent writer to wait.
func TestSoakFirstCollectionDoesNotBlockWriterNearBusyTimeout(t *testing.T) {
	firstCollectionWriterWait(t, 30000)
}
