package ccaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const labelRunes = 40

var (
	// A path is a run starting with "/", "~/", "./" or "../" that follows the
	// start of the text, whitespace, a quote, "(" or "=". Requiring that lead
	// keeps "and/or" intact.
	pathRE   = regexp.MustCompile(`(^|[\s'"(=])((?:~|\.{1,2})?(?:/[^\s':;,)]+)+)`)
	numberRE = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	spaceRE  = regexp.MustCompile(`\s+`)
)

// SigLabel is the short label of a signature: paths become <path>, numbers
// become N, whitespace (including newlines and tabs) collapses to one space,
// and the first 40 runes are kept.
func SigLabel(sig string) string {
	s := pathRE.ReplaceAllString(sig, "${1}<path>")
	s = numberRE.ReplaceAllString(s, "N")
	s = strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
	if s == "" {
		return "(empty)"
	}
	r := []rune(s)
	if len(r) > labelRunes {
		r = r[:labelRunes]
	}
	return string(r)
}

// SigHash is the first 8 hex characters of SHA-256 over the raw signature.
func SigHash(sig string) string {
	sum := sha256.Sum256([]byte(sig))
	return hex.EncodeToString(sum[:])[:8]
}

// SigKey is the subject key of a signature row: sig:<label>:<hash>.
func SigKey(sig string) string {
	return "sig:" + SigLabel(sig) + ":" + SigHash(sig)
}
