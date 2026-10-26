package failsig

import (
	"regexp"
	"strings"
)

// Marker replaces every masked span.
const Marker = "[REDACTED]"

// redaction is one masking pass. Exactly one of repl and fn is set.
type redaction struct {
	name string
	re   *regexp.Regexp
	repl string              // expansion template for ReplaceAllString
	fn   func(string) string // per-match replacement for ReplaceAllStringFunc
}

// redactions run in this order. The multi-line key blocks go first, so that
// the per-line passes never see key material. The anchored shapes (URL
// userinfo, Bearer, prefixed tokens) go before the generic run, so the
// context they keep ("https://", "Bearer ") survives. No pass matches a
// newline, except the two key-block passes, and those put back every newline
// they consume.
var redactions = []redaction{
	// A complete PEM/OpenSSH private-key block. If the BEGIN line has no
	// matching END, because the text was cut mid-key, mask from BEGIN to the
	// end of the text. Failing closed beats leaking the rest of the key.
	{
		name: "private-key-block",
		re:   regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|[\s\S]*)`),
		fn:   keepNewlines,
	},
	// An END line whose BEGIN was cut off (a transcript tail that starts
	// mid-key). Mask the END line and the base64-only lines directly above
	// it, which are the rest of the key body. Otherwise a short final body
	// line, under 32 characters, would slip past the generic run pass.
	{
		name: "private-key-orphan-end",
		re:   regexp.MustCompile(`(?m)(?:^[A-Za-z0-9+/=]*\r?\n)*-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`),
		fn:   keepNewlines,
	},
	// URL userinfo: scheme://user:secret@host becomes scheme://[REDACTED]@host.
	// A user-only form is masked too, because a token is often passed as the
	// username. The span is greedy up to the LAST "@" before the path, so a
	// raw "@" inside a malformed password cannot leave a tail behind.
	{
		name: "url-userinfo",
		re:   regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/?#]+@`),
		repl: "${1}" + Marker + "@",
	},
	// "Bearer <token>", in any case. The scheme word is kept. The token must
	// be at least 8 characters, so prose such as "the bearer of bad news" is
	// left alone. No real bearer credential is that short.
	{
		name: "bearer-token",
		re:   regexp.MustCompile(`(?i)\b(bearer)[ \t]+[A-Za-z0-9._~+/=-]{8,}`),
		repl: "${1} " + Marker,
	},
	// Provider-prefixed tokens: GitHub (ghp_/gho_/ghu_/ghs_/ghr_ and
	// github_pat_), GitLab (glpat-), AWS access key ids (AKIA/ASIA), and
	// sk- API keys. Each prefix is anchored on a word boundary, so "sk-"
	// inside "task-" or "disk-" does not match.
	{
		name: "prefixed-token",
		re:   regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,}|(?:AKIA|ASIA)[0-9A-Z]{16}|sk-[A-Za-z0-9_-]{16,})`),
		repl: Marker,
	},
	// Generic hex, base64, or base64url run of 32 or more characters, plus
	// any trailing "=" padding. This is the catch-all for an unprefixed
	// secret. "=" is allowed only as trailing padding, so the "key" in
	// "key=<secret>" survives. A run with no letter or digit at all (a
	// "-----" separator line) is left alone.
	{
		name: "generic-run",
		re:   regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`),
		fn: func(m string) string {
			if strings.ContainsFunc(m, isAlnum) {
				return Marker
			}
			return m
		},
	},
}

// Redact returns text with credentials masked by [Marker]. It masks URL
// userinfo, Bearer tokens, provider-prefixed tokens, generic 32+ character
// hex/base64 runs, and PEM/OpenSSH private-key blocks.
//
// Redact keeps every newline in text, so line N of the result is line N of
// the input. [Classify] relies on this to find the evidence line. Run Redact
// over the WHOLE text before cutting any of it (see the package doc,
// "Redaction"). Input should be decoded text. A JSON-escaped transcript line
// should be decoded first, so that its "\n" escapes are real newlines.
func Redact(text string) string {
	for _, r := range redactions {
		text = r.apply(text)
	}
	return text
}

// apply runs this one pass over text.
func (r redaction) apply(text string) string {
	if r.fn != nil {
		return r.re.ReplaceAllStringFunc(text, r.fn)
	}
	return r.re.ReplaceAllString(text, r.repl)
}

// keepNewlines masks a multi-line span but keeps as many newlines as it
// had. That preserves the line numbering Redact promises.
func keepNewlines(m string) string {
	return Marker + strings.Repeat("\n", strings.Count(m, "\n"))
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
