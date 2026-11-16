package state

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomID(prefix string) string {
	b := make([]byte, 7) // 56 bits, at least the 50 that 10 characters carry
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(b32.EncodeToString(b))[:10]
}

// NewWorktreeID returns "w-" plus 10 lowercase base32 characters. The caller
// rejects a collision with any record or tombstone.
func NewWorktreeID() string { return randomID("w-") }

// NewLeaseID returns "l-" plus 10 lowercase base32 characters.
func NewLeaseID() string { return randomID("l-") }
