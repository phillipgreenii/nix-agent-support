// Package redact scrubs deny-listed token shapes from text before it is
// staged or stored. Redaction is deterministic and idempotent: a replacement
// marker never matches any deny-list pattern.
package redact

import (
	"regexp"
	"strings"
)

// Class keys reported in Counts and embedded in replacement markers.
const (
	ClassSK       = "sk"
	ClassGHP      = "ghp"
	ClassCloudKey = "cloud-key"
	ClassBearer   = "bearer"
	ClassEnvLine  = "env-line"
	ClassHomePath = "home-path"
)

// Counts maps a class key to the number of redactions made in that class.
// Classes with no redactions are absent.
type Counts map[string]int

// Add merges other into c.
func (c Counts) Add(other Counts) {
	for k, v := range other {
		c[k] += v
	}
}

// Total returns the sum of all class counts.
func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

func marker(class string) string { return "[REDACTED:" + class + "]" }

type rule struct {
	class string
	re    *regexp.Regexp
}

// Order matters: whole-line env assignments first, then token shapes.
var rules = []rule{
	{ClassEnvLine, regexp.MustCompile(`(?m)^[ \t]*[A-Z][A-Z0-9_]*=[^\n]*$`)},
	{ClassSK, regexp.MustCompile(`\bsk-\w{20,}`)},
	{ClassGHP, regexp.MustCompile(`\bghp_[A-Za-z0-9]{36,}`)},
	{ClassCloudKey, regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{ClassBearer, regexp.MustCompile(`\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
}

// homePath matches an absolute home path at a path boundary. Group 1 is the
// preceding character (kept), group 2 the path.
var homePath = regexp.MustCompile("(^|[^A-Za-z0-9_./-])(/(?:Users|home)/[^\\s\"'`<>()\\[\\],;:]*)")

// Text redacts s. A home path under scopeRoot is rewritten relative to it; any
// other home path becomes the home-path marker. scopeRoot may be empty.
func Text(s, scopeRoot string) (string, Counts) {
	counts := Counts{}
	for _, r := range rules {
		n := 0
		s = r.re.ReplaceAllStringFunc(s, func(string) string {
			n++
			return marker(r.class)
		})
		if n > 0 {
			counts[r.class] += n
		}
	}
	root := strings.TrimRight(scopeRoot, "/")
	n := 0
	s = homePath.ReplaceAllStringFunc(s, func(m string) string {
		sub := homePath.FindStringSubmatch(m)
		prefix, p := sub[1], sub[2]
		n++
		if root != "" {
			if p == root {
				return prefix + "."
			}
			if strings.HasPrefix(p, root+"/") {
				return prefix + strings.TrimPrefix(p, root+"/")
			}
		}
		return prefix + marker(ClassHomePath)
	})
	if n > 0 {
		counts[ClassHomePath] += n
	}
	return s, counts
}

// Value redacts every string value in a decoded JSON value (map[string]any,
// []any, string, other scalars). Map keys are untouched. The input is not
// modified; containers are copied.
func Value(v any, scopeRoot string) (any, Counts) {
	counts := Counts{}
	return value(v, scopeRoot, counts), counts
}

func value(v any, root string, counts Counts) any {
	switch t := v.(type) {
	case string:
		out, c := Text(t, root)
		counts.Add(c)
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = value(e, root, counts)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = value(e, root, counts)
		}
		return out
	default:
		return v
	}
}
