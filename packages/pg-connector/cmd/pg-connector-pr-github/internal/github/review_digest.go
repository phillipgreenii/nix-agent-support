package github

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// This file holds the content digest the review_submit write path stamps into
// the body marker, and its verification (entity-change-flow contract 9.1 and
// 9.1a). It lives apart from github.go on purpose: github.go is hash-pinned
// against pg-pr's copy (testdata/pg-pr-drift), and none of this exists in
// pg-pr.
//
// Why a digest at all: the marker alone proves a text was posted by this
// backend, not that it is still unedited. A web-UI or API edit that keeps the
// marker line leaves the text indistinguishable by marker (prerequisite P3),
// and GitHub's lastEditedAt stays null for pending comments (G2), so the only
// way to prove a pending review is still what the backend posted is to carry
// a digest of what it posted, in the one place the host stores verbatim: the
// text itself.
//
// What it covers: the body (marker line removed) and every comment's text, as
// one digest. Comments are folded in as a SORTED list of per-comment hashes,
// so the read order of the comments does not matter, but adding, removing or
// editing any of them, or editing the body, changes the digest. Paths, lines
// and sides are deliberately not covered: they shift when the head advances
// (P8) and a stale review is exactly the one being checked.
//
// Newlines: a web-UI save rewrites LF to CRLF (G2), so every text is
// normalized (CRLF to LF, trailing whitespace trimmed) before it is hashed, at
// post time and at verification time alike. A CRLF-only change is therefore
// NOT an edit.

// DigestMarkerPrefix opens the body marker that carries the digest:
//
//	<!-- pg-connector-pr-github:review digest=sha256:<64 hex> -->
//
// It is the body's authorship marker (a body carrying it counts as marked).
// Comments carry the plain BotMarker.
const DigestMarkerPrefix = "<!-- pg-connector-pr-github:review digest=sha256:"

const digestMarkerSuffix = " -->"

// digestMarkerRE matches one complete digest marker and the newline before it.
var digestMarkerRE = regexp.MustCompile(`\n?<!-- pg-connector-pr-github:review digest=sha256:(\S*) -->`)

// digestHexRE is the only digest spelling the verifier accepts.
var digestHexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DigestState is the verification outcome for a pending review's content.
type DigestState string

const (
	// DigestVerified: the body carries a readable digest and it matches the
	// body and every comment as read back. The content is unedited.
	DigestVerified DigestState = "verified"
	// DigestMissing: the body carries no digest marker (a review posted before
	// digests existed, by pg-pr, or by a human). Not verified-unedited.
	DigestMissing DigestState = "missing"
	// DigestUnreadable: a digest marker is present but is damaged, duplicated
	// or not a sha256 hex. Not verified-unedited.
	DigestUnreadable DigestState = "unreadable"
	// DigestMismatch: a readable digest that does not match the content: the
	// text was edited, or a comment was added or removed.
	DigestMismatch DigestState = "mismatch"
)

// NormalizeText is the canonical form hashed for a text: CRLF to LF and
// trailing whitespace trimmed.
func NormalizeText(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\r\n")
}

// ReviewDigest returns the hex sha256 digest of a review: body is the body
// text WITHOUT its digest marker, comments are the comment texts exactly as
// posted. Both are normalized here.
func ReviewDigest(body string, comments []string) string {
	hs := make([]string, 0, len(comments))
	for _, c := range comments {
		hs = append(hs, textHash(c))
	}
	sort.Strings(hs)
	h := sha256.New()
	h.Write([]byte("pg-connector-pr-github review digest v1\nbody:" + textHash(body) + "\n"))
	for _, c := range hs {
		h.Write([]byte("comment:" + c + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(NormalizeText(s)))
	return hex.EncodeToString(sum[:])
}

// StampBodyWithDigest returns the body to post: body with the visible
// attribution (unless it already carries a marker), then the digest marker on
// its own line. commentTexts are the comment texts exactly as they will be
// posted (already marker-stamped). Any digest marker the caller put in body is
// removed first, so a caller cannot supply a digest of its own.
func StampBodyWithDigest(body string, commentTexts []string) string {
	body = digestMarkerRE.ReplaceAllString(body, "")
	core := body
	if !strings.Contains(core, BotMarker) {
		if core == "" {
			core = botAttribution
		} else {
			core = core + "\n\n" + botAttribution
		}
	}
	core = strings.TrimRight(core, " \t\r\n")
	return core + "\n" + DigestMarkerPrefix + ReviewDigest(core, commentTexts) + digestMarkerSuffix
}

// VerifyDigest classifies a pending review's content as read back from the
// host. body and comments are the texts as read. It is fail-closed: only
// DigestVerified means the content is provably what the backend posted.
func VerifyDigest(body string, comments []string) DigestState {
	norm := strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.Contains(norm, DigestMarkerPrefix) {
		return DigestMissing
	}
	locs := digestMarkerRE.FindAllStringSubmatchIndex(norm, -1)
	if len(locs) != 1 || strings.Count(norm, DigestMarkerPrefix) != 1 {
		return DigestUnreadable
	}
	loc := locs[0]
	hexDigest := norm[loc[2]:loc[3]]
	if !digestHexRE.MatchString(hexDigest) {
		return DigestUnreadable
	}
	core := norm[:loc[0]] + norm[loc[1]:]
	if ReviewDigest(core, comments) != hexDigest {
		return DigestMismatch
	}
	return DigestVerified
}
