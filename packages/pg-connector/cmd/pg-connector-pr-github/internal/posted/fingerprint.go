// Package posted holds the offline building blocks of the review
// create-or-append idempotence: the per-comment fingerprint and its hidden
// marker, the per-PR posted-sidecar (dismissal memory), the pure classifier
// that sorts a request's comments into already_present, dismissed and
// to_write, and the per-PR advisory lock. None of it touches GitHub: the
// caller reads the viewer's markers from GitHub and hands them in.
//
// Exported API (downstream packets read these names from this comment):
//
// Fingerprint and marker (fingerprint.go):
//
//	NormalizeBody(body string) string
//	PointFingerprint(path, side string, line int, body string) string
//	ReplyFingerprint(threadID, body string) string
//	Marker(fp string) string                  // "<!-- pg-fp:<fp> -->"
//	ExtractFingerprints(text string) []string // every marker in text, in order
//	FingerprintLen                            // 16 hex characters
//
// Sidecar (sidecar.go):
//
//	type State  { Version; Fingerprints; BodyHeads; LastAppend *LastAppend }
//	type LastAppend { At; Added; Head }
//	type Store  { Dir string }                // Dir is the posted/ directory
//	StateHomeFromEnv(getenv) string           // backend state home
//	StoreFromEnv(getenv) Store
//	Store.Path(owner, repo string, pr int) (string, error)
//	Store.Load(owner, repo string, pr int) (State, error) // missing file = empty State
//	Store.Save(owner, repo string, pr int, st State) error // atomic temp + rename
//	State.Has(fp) bool; State.AddFingerprints(fps...); State.AddBodyHead(head)
//	ErrCorrupt                                // Load error for an undecodable file
//	EnvStateDir = "PG_CONNECTOR_PR_GITHUB_STATE_DIR"
//
// Body section (section.go):
//
//	SectionHeadLen                            // 12 hex characters
//	SectionClose                              // "<!-- /pg-section -->"
//	SectionOpen(head string) string           // "<!-- pg-section head=<sha12> -->"
//	FindSection(body, head string) (start, end int, ok bool)
//	                                          // body[start:end] is the whole section
//	                                          // for head, delimiters included
//
// Classifier (classify.go):
//
//	type Verdict string: AlreadyPresent, Dismissed, ToWrite
//	Classify(requested []string, onGitHub map[string]bool, sc State) []Verdict
//
// Lock (lock.go):
//
//	type Locker { Dir string; Wait time.Duration; Poll time.Duration }
//	LockerFromEnv(getenv) Locker              // Wait = LockWait (60s)
//	Locker.Acquire(owner, repo string, pr int) (*Lock, error)
//	Lock.Release() error
//	ErrUnavailable                            // timeout; caller maps it to the
//	                                          // retryable taxonomy code "unavailable"
//	                                          // (INV-ERR-1)
//
// Fingerprints are an exact-replay key, not a semantic one. A new point is the
// first 16 hex characters of SHA-256(path "\n" SIDE "\n" line "\n"
// normalizedBody) with SIDE upper-cased ("" is RIGHT). A reply uses the same
// 16-hex truncation over SHA-256(thread_id "\n" normalizedBody). The head sha
// is deliberately not part of either, so a point is not posted twice across
// heads; the two honest limits are that a point whose line moved is a
// different fingerprint, and identical text on different code at the same
// path and line is suppressed.
package posted

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// FingerprintLen is the number of hex characters in a fingerprint.
const FingerprintLen = 16

// NormalizeBody converts CRLF to LF and trims the trailing whitespace of the
// whole body, so a web-UI save (which rewrites LF to CRLF) does not change the
// fingerprint.
func NormalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return strings.TrimRight(body, " \t\r\n\v\f")
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:FingerprintLen]
}

// PointFingerprint fingerprints a NEW point. side is upper-cased; "" is RIGHT.
func PointFingerprint(path, side string, line int, body string) string {
	side = strings.ToUpper(side)
	if side == "" {
		side = "RIGHT"
	}
	return digest(path + "\n" + side + "\n" + strconv.Itoa(line) + "\n" + NormalizeBody(body))
}

// ReplyFingerprint fingerprints a REPLY to an existing thread.
func ReplyFingerprint(threadID, body string) string {
	return digest(threadID + "\n" + NormalizeBody(body))
}

// Marker is the hidden marker a comment body carries: <!-- pg-fp:<fp> -->.
func Marker(fp string) string {
	return "<!-- pg-fp:" + fp + " -->"
}

var markerRE = regexp.MustCompile(`<!-- pg-fp:([0-9a-f]{` + strconv.Itoa(FingerprintLen) + `}) -->`)

// ExtractFingerprints returns every fingerprint marker found in text, in
// order of appearance (duplicates preserved).
func ExtractFingerprints(text string) []string {
	var out []string
	for _, m := range markerRE.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}
