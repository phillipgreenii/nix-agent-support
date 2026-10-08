package event

import (
	"fmt"
	"io"
	"time"
)

// ID is a ULID: 26 characters of Crockford base32, the first ten a
// millisecond timestamp, so ids drawn at different milliseconds sort by time.
type ID string

// crockford is the ULID alphabet: digits and upper-case letters without I, L,
// O and U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const idLength = 26

// NewID returns a ULID for the millisecond of t, with 80 random bits read from
// entropy. A time before 1970 is taken as the epoch. It panics when entropy
// cannot supply ten bytes, because an id built from missing entropy could
// repeat; the injected sources (crypto/rand, a seeded math/rand) do not fail.
func NewID(t time.Time, entropy io.Reader) ID {
	ms := t.UnixMilli()
	if ms < 0 {
		ms = 0
	}
	var raw [16]byte
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	if _, err := io.ReadFull(entropy, raw[6:]); err != nil {
		panic(fmt.Sprintf("event.NewID: reading entropy: %v", err))
	}
	// 128 bits are written as 26 groups of 5 bits, the first group holding
	// only the top 3 bits.
	var out [idLength]byte
	var acc uint16
	bits := 0
	pos := idLength - 1
	for i := len(raw) - 1; i >= 0; i-- {
		acc |= uint16(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&31]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		out[pos] = crockford[acc&31]
	}
	return ID(out[:])
}

// ParseID checks that s is a well-formed ULID: 26 upper-case Crockford
// characters whose value fits in 128 bits.
func ParseID(s string) (ID, error) {
	if len(s) != idLength {
		return "", fmt.Errorf("id %q is %d bytes long, want a 26-character ULID", s, len(s))
	}
	for i := 0; i < len(s); i++ {
		if !crockfordChar(s[i]) {
			return "", fmt.Errorf("id %q holds %q at position %d, which is not an upper-case Crockford base32 character (no I, L, O or U)", s, s[i], i)
		}
	}
	if s[0] > '7' {
		return "", fmt.Errorf("id %q starts above 7 and does not fit in 128 bits", s)
	}
	return ID(s), nil
}

func crockfordChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return c != 'I' && c != 'L' && c != 'O' && c != 'U'
	}
	return false
}
