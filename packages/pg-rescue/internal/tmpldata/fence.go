package tmpldata

import "strings"

// minFence is the shortest code fence worth using.
const minFence = 3

// Delimiter returns the backtick run that safely fences content: longer than
// the longest run of backticks anywhere in content (and at least three), so
// nothing inside can close the fence early.
func Delimiter(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(minFence, longest+1))
}

// Block wraps untrusted content in a labelled code fence:
//
//	<delimiter> quoted-data <nonce>
//	<content>
//	<delimiter>
//
// The delimiter is longer than any backtick run in content, so the content
// cannot close the fence; the label carries the per-run nonce, which the
// content's author cannot know, so the opening line cannot be forged either.
// It frames the information as quoted data; it restricts nothing.
func Block(content, nonce string) string {
	delim := Delimiter(content)
	var b strings.Builder
	b.WriteString(delim + " quoted-data " + nonce + "\n")
	if content != "" {
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(delim + "\n")
	return b.String()
}
