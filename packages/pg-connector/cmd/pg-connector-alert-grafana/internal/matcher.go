// matcher.go: translates one Grafana query element (design 5.3: an
// Alertmanager matcher set, `{name op "value", ...}`) into the repeated
// `filter=` parameters the Alertmanager v2 alerts call accepts. Operators
// are =, !=, =~ and !~; comma-joined matchers are ANDed (each becomes its own
// filter= parameter, and Alertmanager ANDs repeated filters).
package internal

import (
	"fmt"
	"strings"
)

// translateMatcherSet parses one query element and returns the filter=
// values it maps to, one per matcher, in source order. An empty element or an
// empty set (`{}`) yields no filters, i.e. the whole firing set. A malformed
// element is an error the caller reports as invalid_argument.
//
// Values are re-emitted double-quoted with backslash and double-quote
// escaped, regardless of whether the operator wrote them quoted, so
// Alertmanager's own matcher parser sees one canonical form.
func translateMatcherSet(element string) ([]string, error) {
	s := strings.TrimSpace(element)
	if s == "" {
		return nil, nil
	}
	hasOpen, hasClose := strings.HasPrefix(s, "{"), strings.HasSuffix(s, "}")
	if hasOpen != hasClose {
		return nil, fmt.Errorf("matcher set %q has unbalanced braces", element)
	}
	if hasOpen {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		return nil, nil
	}

	parts, err := splitMatchers(s)
	if err != nil {
		return nil, fmt.Errorf("matcher set %q: %w", element, err)
	}
	filters := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := translateMatcher(p)
		if err != nil {
			return nil, fmt.Errorf("matcher set %q: %w", element, err)
		}
		filters = append(filters, f)
	}
	return filters, nil
}

// splitMatchers splits s on commas that are outside double quotes (a quoted
// value may itself contain commas, e.g. a regex alternation).
func splitMatchers(s string) ([]string, error) {
	var (
		parts   []string
		cur     strings.Builder
		inQuote bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			cur.WriteRune(r)
		case inQuote && r == '\\':
			escaped = true
			cur.WriteRune(r)
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quoted value")
	}
	parts = append(parts, cur.String())
	return parts, nil
}

// translateMatcher parses one `name op value` matcher.
func translateMatcher(m string) (string, error) {
	nameEnd := strings.IndexAny(m, "=!~")
	if nameEnd <= 0 {
		return "", fmt.Errorf("matcher %q has no label name or operator", m)
	}
	name := strings.TrimSpace(m[:nameEnd])
	if name == "" || strings.ContainsAny(name, " \t\"") {
		return "", fmt.Errorf("matcher %q has an invalid label name", m)
	}
	rest := m[nameEnd:]

	var op string
	for _, cand := range []string{"=~", "!~", "!=", "="} {
		if strings.HasPrefix(rest, cand) {
			op = cand
			break
		}
	}
	if op == "" {
		return "", fmt.Errorf("matcher %q has an unrecognized operator (want =, !=, =~ or !~)", m)
	}

	value, err := parseMatcherValue(strings.TrimSpace(rest[len(op):]))
	if err != nil {
		return "", fmt.Errorf("matcher %q: %w", m, err)
	}
	return name + op + quoteMatcherValue(value), nil
}

// parseMatcherValue unquotes a double-quoted value (honouring \" and \\
// escapes) or returns an unquoted value verbatim.
func parseMatcherValue(v string) (string, error) {
	if !strings.HasPrefix(v, `"`) {
		if strings.Contains(v, `"`) {
			return "", fmt.Errorf("stray quote in unquoted value")
		}
		return v, nil
	}
	if len(v) < 2 || !strings.HasSuffix(v, `"`) {
		return "", fmt.Errorf("unterminated quoted value")
	}
	inner := v[1 : len(v)-1]
	var b strings.Builder
	escaped := false
	for _, r := range inner {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			return "", fmt.Errorf("unescaped quote inside quoted value")
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		return "", fmt.Errorf("dangling backslash in quoted value")
	}
	return b.String(), nil
}

// quoteMatcherValue renders value as a double-quoted Alertmanager matcher
// value.
func quoteMatcherValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
