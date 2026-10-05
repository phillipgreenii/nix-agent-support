package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// This file is `pg-desk doctor --router-config`'s reader for the few keys of
// a pg-router config doctor needs [design: 9.9]: each `[[query]]`'s name,
// timer period (`trigger = { kind = "period", every = "60s" }`) and command
// argv, and each `[[role]]`'s name, enabled flag and `binds` list. The file
// is read as a file: pg-desk takes no dependency on pg-router, and no TOML
// library, so this is a deliberately small reader for the TOML subset that
// shape uses (tables, arrays of tables, strings, booleans, numbers, arrays,
// inline tables, comments, multi-line arrays). Anything it does not
// understand it ignores; a malformed value of a key it DOES read is an error.

// routerQuery is one `[[query]]` of the router config.
type routerQuery struct {
	Name string
	// Every is trigger.every when the trigger is a period; zero when absent.
	Every time.Duration
	Argv  []string
}

// routerRole is one `[[role]]` of the router config.
type routerRole struct {
	Name    string
	Enabled bool
	Binds   []string
}

// routerConfig is what doctor reads out of the router config.
type routerConfig struct {
	Queries []routerQuery
	Roles   []routerRole
}

// consumerFlagValues returns every value of a `--consumer <v>` or
// `--consumer=<v>` pair in argv.
func consumerFlagValues(argv []string) []string {
	var out []string
	for i, a := range argv {
		switch {
		case a == "--consumer" && i+1 < len(argv):
			out = append(out, argv[i+1])
		case strings.HasPrefix(a, "--consumer="):
			out = append(out, strings.TrimPrefix(a, "--consumer="))
		}
	}
	return out
}

func argvNamesType(argv []string, entityType string) bool {
	for _, a := range argv {
		if a == entityType {
			return true
		}
	}
	return false
}

// QueriesFor returns the router queries whose command argv names the entity
// type AND carries a `--consumer` flag — the one matching rule shared by the
// stalled-consumer check (with consumer set: that exact consumer) and the
// sweep bound (consumer empty: any consumer) [design: 9.9, 8.4].
func (rc *routerConfig) QueriesFor(entityType, consumer string) []routerQuery {
	var out []routerQuery
	for _, q := range rc.Queries {
		if !argvNamesType(q.Argv, entityType) {
			continue
		}
		consumers := consumerFlagValues(q.Argv)
		if len(consumers) == 0 {
			continue
		}
		if consumer != "" {
			found := false
			for _, c := range consumers {
				if c == consumer {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, q)
	}
	return out
}

// PollInterval is the smallest period among the router queries matching the
// type (any consumer); ok is false when none matches or none has a period.
func (rc *routerConfig) PollInterval(entityType string) (time.Duration, bool) {
	var best time.Duration
	for _, q := range rc.QueriesFor(entityType, "") {
		if q.Every > 0 && (best == 0 || q.Every < best) {
			best = q.Every
		}
	}
	return best, best > 0
}

// ConsumerPeriod is the period of the router query naming this consumer and
// type (smallest when several); ok is false when none matches.
func (rc *routerConfig) ConsumerPeriod(entityType, consumer string) (time.Duration, bool) {
	var best time.Duration
	for _, q := range rc.QueriesFor(entityType, consumer) {
		if q.Every > 0 && (best == 0 || q.Every < best) {
			best = q.Every
		}
	}
	return best, best > 0
}

// RolesBoundTo returns the roles one of whose binds starts with "<type>.":
// exact string matching on the kind prefix, no wildcard (S22).
func (rc *routerConfig) RolesBoundTo(entityType string) []routerRole {
	var out []routerRole
	for _, r := range rc.Roles {
		for _, b := range r.Binds {
			if strings.HasPrefix(b, entityType+".") {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// loadRouterConfig reads and parses the router config at path.
func loadRouterConfig(path string) (*routerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseRouterConfig(string(b))
}

type tomlTable = map[string]any

// parseRouterConfig parses the TOML subset described at the top of this file.
func parseRouterConfig(text string) (*routerConfig, error) {
	var queries, roles []tomlTable
	var cur tomlTable // table receiving key = value lines (nil: ignored section)

	logical, err := tomlLogicalLines(text)
	if err != nil {
		return nil, err
	}
	for _, line := range logical {
		if strings.HasPrefix(line, "[") {
			name, array, ok := parseTomlHeader(line)
			if !ok {
				return nil, fmt.Errorf("router config: malformed table header %q", line)
			}
			cur = nil
			switch {
			case array && name == "query":
				queries = append(queries, tomlTable{})
				cur = queries[len(queries)-1]
			case array && name == "role":
				roles = append(roles, tomlTable{})
				cur = roles[len(roles)-1]
			case !array && (name == "query.command" || name == "query.trigger") && len(queries) > 0:
				sub := tomlTable{}
				queries[len(queries)-1][strings.TrimPrefix(name, "query.")] = sub
				cur = sub
			}
			continue
		}
		key, rawVal, ok := splitTomlKeyValue(line)
		if !ok {
			return nil, fmt.Errorf("router config: expected key = value, got %q", line)
		}
		if cur == nil {
			continue
		}
		val, err := parseTomlValue(rawVal)
		if err != nil {
			return nil, fmt.Errorf("router config: key %q: %w", key, err)
		}
		cur[key] = val
	}

	rc := &routerConfig{}
	for _, q := range queries {
		rq := routerQuery{}
		rq.Name, _ = q["name"].(string)
		if trig, ok := q["trigger"].(tomlTable); ok {
			if every, ok := trig["every"].(string); ok && every != "" {
				d, err := time.ParseDuration(every)
				if err != nil || d <= 0 {
					return nil, fmt.Errorf("router config: query %q: trigger.every %q is not a positive duration", rq.Name, every)
				}
				rq.Every = d
			}
		}
		if cmd, ok := q["command"].(tomlTable); ok {
			rq.Argv = stringList(cmd["argv"])
		}
		rc.Queries = append(rc.Queries, rq)
	}
	for _, r := range roles {
		rr := routerRole{Enabled: true}
		rr.Name, _ = r["name"].(string)
		if en, ok := r["enabled"].(bool); ok {
			rr.Enabled = en
		}
		rr.Binds = stringList(r["binds"])
		rc.Roles = append(rc.Roles, rr)
	}
	return rc, nil
}

func stringList(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// stripTomlComment removes a trailing # comment that is outside any string.
func stripTomlComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		switch {
		case quote == '"' && escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// bracketDepth is the net count of unclosed [ and { outside strings.
func bracketDepth(s string) int {
	depth := 0
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case quote == '"' && escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '[' || r == '{':
			depth++
		case r == ']' || r == '}':
			depth--
		}
	}
	return depth
}

// tomlLogicalLines strips comments and blank lines and joins multi-line
// values (arrays or inline tables spanning lines) into single lines. Table
// headers are never joined.
func tomlLogicalLines(text string) ([]string, error) {
	var out []string
	var acc strings.Builder
	depth := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(stripTomlComment(raw))
		if line == "" {
			continue
		}
		if depth == 0 {
			acc.Reset()
			acc.WriteString(line)
			if !strings.HasPrefix(line, "[") {
				depth = bracketDepth(line)
			}
		} else {
			acc.WriteString(" ")
			acc.WriteString(line)
			depth = bracketDepth(acc.String())
		}
		if depth <= 0 {
			out = append(out, acc.String())
			depth = 0
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("router config: unterminated array or inline table")
	}
	return out, nil
}

// parseTomlHeader parses "[a.b]" or "[[a.b]]".
func parseTomlHeader(line string) (name string, array, ok bool) {
	switch {
	case strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]"):
		return strings.TrimSpace(line[2 : len(line)-2]), true, true
	case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
		return strings.TrimSpace(line[1 : len(line)-1]), false, true
	}
	return "", false, false
}

// splitTomlKeyValue splits "key = value" at the first '=' outside quotes.
func splitTomlKeyValue(line string) (key, value string, ok bool) {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '=':
			k := strings.Trim(strings.TrimSpace(line[:i]), `"'`)
			return k, strings.TrimSpace(line[i+1:]), k != ""
		}
	}
	return "", "", false
}

// splitTopLevel splits s on commas that are outside strings and brackets.
func splitTopLevel(s string) []string {
	var parts []string
	var quote rune
	escaped := false
	depth, start := 0, 0
	for i, r := range s {
		switch {
		case quote == '"' && escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '[' || r == '{':
			depth++
		case r == ']' || r == '}':
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// parseTomlValue parses a string, boolean, number, array or inline table.
func parseTomlValue(s string) (any, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return nil, fmt.Errorf("empty value")
	case s[0] == '"':
		v, err := strconv.Unquote(s)
		if err != nil {
			return nil, fmt.Errorf("bad string %s", s)
		}
		return v, nil
	case s[0] == '\'':
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return nil, fmt.Errorf("bad literal string %s", s)
		}
		return s[1 : len(s)-1], nil
	case s[0] == '[':
		if s[len(s)-1] != ']' {
			return nil, fmt.Errorf("bad array %s", s)
		}
		items := []any{}
		for _, p := range splitTopLevel(s[1 : len(s)-1]) {
			if strings.TrimSpace(p) == "" {
				continue // empty array or trailing comma
			}
			v, err := parseTomlValue(p)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	case s[0] == '{':
		if s[len(s)-1] != '}' {
			return nil, fmt.Errorf("bad inline table %s", s)
		}
		tbl := tomlTable{}
		for _, p := range splitTopLevel(s[1 : len(s)-1]) {
			if strings.TrimSpace(p) == "" {
				continue
			}
			k, rawV, ok := splitTomlKeyValue(strings.TrimSpace(p))
			if !ok {
				return nil, fmt.Errorf("bad inline table entry %q", p)
			}
			v, err := parseTomlValue(rawV)
			if err != nil {
				return nil, err
			}
			tbl[k] = v
		}
		return tbl, nil
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	}
	return s, nil // number, date or other bare scalar: kept raw, never read
}
