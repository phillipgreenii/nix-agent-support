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

	// sigInputEcho is pg2-z38lk item 3: an InputValidationError body echoes the
	// malformed tool-input PAYLOAD back verbatim ("You sent (first N of N
	// bytes): <payload>"), and the payload differs on every call by
	// construction — different byte counts, different echoed JSON. Measured in
	// the 2026-08-31 baseline census, one systemic malformed-tool-input class
	// fragmented across >=15 findings (ranks 144, 190, 219, 260, 393, 643-645,
	// 1226, 1241, 1242, 1253, 1257, 1258, 1265) because the generic
	// number/path/hash collapses above cannot touch arbitrary echoed JSON
	// tokens, so no single row ever ranked and the class stayed invisible.
	//
	// This MUST run BEFORE truncateRunes, not after: SignatureWindow keeps only
	// the first 600 runes, and the echoed payload can push the "You sent"
	// marker itself — and everything after it — past that cut, leaving a
	// half-echoed, still-variable tail in the signature. Cutting on the full
	// body first means the marker is found and collapsed wherever it falls,
	// then truncation (and the collapses below) operate on an already-short,
	// deterministic string.
	//
	// The byte counts collapse to the literal N (not re-derived from sigNumber,
	// which would also fire on the SAME text — being explicit here is what
	// makes the placeholder "N of N" readable as the two counts it replaces,
	// rather than an accidental side effect of an unrelated rule). Text BEFORE
	// the marker — the specific validation failure, e.g. which tool/field was
	// wrong — is left untouched, so two DIFFERENT error kinds still produce two
	// different signatures; only the volatile echo collapses.
	sigInputEcho = regexp.MustCompile(`(?s)You sent \(first \d+ of \d+ bytes\):.*`)
)

// Signature collapses an error body into a comparable grouping key. It is
// computed AT INGEST and stored as a column (T-6) so signature grouping is a
// cheap GROUP BY and — more importantly — is STABLE across runs: recomputing it
// at query time would silently regroup history the moment the normalizer
// changed.
func Signature(body string) string {
	s := sigInputEcho.ReplaceAllString(body, "You sent (first N of N bytes): ECHO")
	s = truncateRunes(s, SignatureWindow)
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
