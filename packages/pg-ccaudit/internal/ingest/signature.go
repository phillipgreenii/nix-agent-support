package ingest

import (
	"regexp"
	"strings"
)

// SignatureWindow is how much of the raw error body feeds the normalized
// signature (T-6), in RUNES.
//
// The stored `content` is untruncated (T-3); only this DERIVED grouping key uses
// a window. 600 is not arbitrary: the differential baseline this ingester is
// checked against normalized a 600-character error body, so keeping the same
// window keeps signature grouping comparable with the census that motivated the
// index. It also bounds cardinality — an unbounded key over multi-kilobyte
// stack traces would group nothing.
//
// The window is applied to RUNES, not bytes, so it can never split a multi-byte
// character (the byte-oriented shell prototype could, which is one of the
// portability traps T-7 exists to retire).
const SignatureWindow = 600

// The normalizer is an ordered port of the prototype's normalize.pl. Order is
// load-bearing: PATH must collapse before numbers, or a path containing digits
// yields a different key than the same path without them.
var (
	sigWhitespace = regexp.MustCompile(`\s+`)
	sigHash       = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	sigToolID     = regexp.MustCompile(`\btoolu_[A-Za-z0-9]+`)
	sigBead       = regexp.MustCompile(`\bpg2-[a-z0-9.]+`)
	sigPath       = regexp.MustCompile(`/[\w.@+-]+(?:/[\w.@+-]+)+`)
	sigNumber     = regexp.MustCompile(`\b\d[\d,.]*\b`)
)

// Signature collapses an error body into a comparable grouping key. It is
// computed AT INGEST and stored as a column (T-6) so signature grouping is a
// cheap GROUP BY and — more importantly — is STABLE across runs: recomputing it
// at query time would silently regroup history the moment the normalizer
// changed.
func Signature(body string) string {
	s := truncateRunes(body, SignatureWindow)
	s = sigWhitespace.ReplaceAllString(s, " ")
	s = sigHash.ReplaceAllString(s, "HASH")
	s = sigToolID.ReplaceAllString(s, "TOOLID")
	s = sigBead.ReplaceAllString(s, "BEAD")
	s = sigPath.ReplaceAllString(s, "PATH")
	s = sigNumber.ReplaceAllString(s, "N")
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
