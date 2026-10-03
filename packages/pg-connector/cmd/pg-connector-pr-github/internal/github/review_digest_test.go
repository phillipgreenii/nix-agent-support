package github

import (
	"strings"
	"testing"
)

// postedForTest builds the body and comment texts exactly as PostPendingReview
// would post them.
func postedForTest(body string, comments ...string) (string, []string) {
	texts := make([]string, 0, len(comments))
	for _, c := range comments {
		texts = append(texts, StampBotMarker(c))
	}
	return StampBodyWithDigest(body, texts), texts
}

func toCRLF(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// TestVerifyDigestClassification is the edit-detection table (prerequisites P3
// and G2): untouched is unedited; a marker-preserving text edit, a removed
// marker, an added unmarked comment, a removed comment and an edited body are
// not; a CRLF-only conversion IS still unedited (a web-UI save rewrites LF to
// CRLF without changing the text).
func TestVerifyDigestClassification(t *testing.T) {
	body, comments := postedForTest("summary line\nsecond line", "finding one", "finding two")

	cases := map[string]struct {
		body     string
		comments []string
		want     DigestState
	}{
		"untouched": {body, comments, DigestVerified},
		"comments in another order": {
			body, []string{comments[1], comments[0]}, DigestVerified,
		},
		"CRLF-converted body, otherwise unchanged": {toCRLF(body), comments, DigestVerified},
		"CRLF-converted comments, otherwise unchanged": {
			body, []string{toCRLF(comments[0]), toCRLF(comments[1])}, DigestVerified,
		},
		"trailing newline added by the host": {body + "\n", comments, DigestVerified},
		"marker-preserving comment text edit": {
			body, []string{strings.Replace(comments[0], "finding one", "finding 1 (edited)", 1), comments[1]}, DigestMismatch,
		},
		"marker removed from a comment": {
			body, []string{strings.Replace(comments[0], BotMarker, "", 1), comments[1]}, DigestMismatch,
		},
		"unmarked comment added": {
			body, append([]string{"a human added this"}, comments...), DigestMismatch,
		},
		"comment removed": {body, comments[:1], DigestMismatch},
		"body text edited, digest marker kept": {
			strings.Replace(body, "summary line", "summary EDITED", 1), comments, DigestMismatch,
		},
		"text appended after the digest marker": {body + "\nhuman note", comments, DigestMismatch},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := VerifyDigest(c.body, c.comments); got != c.want {
				t.Errorf("VerifyDigest = %q, want %q", got, c.want)
			}
		})
	}
}

// TestVerifyDigestNotVerifiedUnedited: a review with no readable digest is
// never verified (fail-closed).
func TestVerifyDigestNotVerifiedUnedited(t *testing.T) {
	body, comments := postedForTest("summary", "finding")
	cases := map[string]struct {
		body string
		want DigestState
	}{
		"no digest, plain marker only": {"summary\n" + BotMarker, DigestMissing},
		"no digest, pg-pr marker":      {"summary\n<!-- pg-pr -->", DigestMissing},
		"empty body":                   {"", DigestMissing},
		"digest marker deleted by hand": {
			strings.Replace(body, body[strings.Index(body, DigestMarkerPrefix):], "", 1), DigestMissing,
		},
		"digest damaged: not hex":         {DigestMarkerPrefix + "zzzz -->", DigestUnreadable},
		"digest damaged: truncated":       {"summary\n" + DigestMarkerPrefix + "abc123 -->", DigestUnreadable},
		"digest damaged: marker unclosed": {"summary\n" + DigestMarkerPrefix + strings.Repeat("a", 64), DigestUnreadable},
		"digest damaged: no space":        {"summary\n" + DigestMarkerPrefix + strings.Repeat("a", 64) + "-->", DigestUnreadable},
		"digest damaged: uppercase hex":   {"summary\n" + DigestMarkerPrefix + strings.Repeat("A", 64) + " -->", DigestUnreadable},
		"two digest markers":              {body + "\n" + DigestMarkerPrefix + strings.Repeat("a", 64) + " -->", DigestUnreadable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := VerifyDigest(c.body, comments)
			if got != c.want {
				t.Errorf("VerifyDigest = %q, want %q", got, c.want)
			}
			if got == DigestVerified {
				t.Errorf("must never be verified")
			}
		})
	}
}

// TestStampBodyWithDigestShape: the digest marker is its own final line, a
// caller cannot supply a digest, and an existing plain marker is not doubled.
func TestStampBodyWithDigestShape(t *testing.T) {
	got := StampBodyWithDigest("top", nil)
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, DigestMarkerPrefix) || !strings.HasSuffix(last, " -->") {
		t.Errorf("digest marker must be the final line: %q", got)
	}
	if !strings.Contains(got, botAttribution) || !strings.HasPrefix(got, "top") {
		t.Errorf("body must keep its text and gain the attribution: %q", got)
	}
	if !strings.HasPrefix(StampBodyWithDigest("", nil), botAttribution) {
		t.Errorf("an empty body must still be stamped")
	}

	forged := "top\n" + DigestMarkerPrefix + strings.Repeat("0", 64) + " -->"
	stamped := StampBodyWithDigest(forged, nil)
	if strings.Count(stamped, DigestMarkerPrefix) != 1 || VerifyDigest(stamped, nil) != DigestVerified {
		t.Errorf("a caller-supplied digest must be dropped, leaving one verifying marker: %q", stamped)
	}

	withPlain := StampBodyWithDigest("x\n"+BotMarker, nil)
	if strings.Count(withPlain, BotMarker) != 1 || strings.Contains(withPlain, botAttribution) {
		t.Errorf("a body already carrying the plain marker must not be re-attributed: %q", withPlain)
	}
	if VerifyDigest(withPlain, nil) != DigestVerified {
		t.Errorf("must verify: %q", withPlain)
	}
}

// TestReviewDigestDependsOnEveryText: the digest covers the body and every
// comment text, and only texts (so comments shifting path or line when the head
// advances, P8, are not edits -- the function takes no anchors at all).
func TestReviewDigestDependsOnEveryText(t *testing.T) {
	base := ReviewDigest("b", []string{"c1", "c2"})
	for name, other := range map[string]string{
		"body":           ReviewDigest("b2", []string{"c1", "c2"}),
		"a comment":      ReviewDigest("b", []string{"c1", "c3"}),
		"fewer":          ReviewDigest("b", []string{"c1"}),
		"more":           ReviewDigest("b", []string{"c1", "c2", "c2"}),
		"body<->comment": ReviewDigest("c1", []string{"b", "c2"}),
	} {
		if other == base {
			t.Errorf("digest must change when %s changes", name)
		}
	}
	if ReviewDigest("b", []string{"c2", "c1"}) != base {
		t.Errorf("comment order must not matter")
	}
}
