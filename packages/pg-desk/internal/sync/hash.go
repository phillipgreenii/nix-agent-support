package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// contentHash renders v as canonical JSON (struct field order, which is
// fixed by declaration order — never a map, whose key order is not stable)
// and returns its sha256 hex digest. Used for the ledger's
// last_synced_content_hash column: see sync.go's package doc ("Planned
// rows and the ledger's existing columns") for why this is what makes
// plan/apply parity mechanically checkable. Never fails: every caller
// passes a plain struct of strings/bools/slices thereof, which json.Marshal
// cannot error on.
func contentHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for this package's own call sites (see doc comment),
		// but a fixed fallback (rather than a panic) keeps this function
		// total.
		b = []byte(err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
