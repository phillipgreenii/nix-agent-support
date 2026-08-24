// Command pa-monitor-decorator-scope is a GENERIC path->scope label
// decorator for pa-monitor. It is launched by the pa-monitor daemon at every
// metric tick with PA_MONITOR_DECORATE=1 in the environment and the session's
// JSON on stdin (see packages/pa-monitor/internal/labels/decorator.go in this
// repo).
//
// It maps the session's CWD to a `workspace.scope` label by LONGEST-prefix
// match over a set of prefix->scope rules supplied by config at launch. The
// binary is deliberately generic: it hardcodes NO paths or organisation
// names. Rules come from repeatable `-rule PREFIX=SCOPE` flags and, as a
// fallback, the PA_MONITOR_SCOPE_RULES env var (`PREFIX=SCOPE` entries
// separated by ';').
//
// On a match it emits `{"labels":{"workspace.scope":"<scope>"}}`. On no match
// (or no rules, or empty stdin) it emits empty labels, so the daemon's
// DefaultScope ("personal") stands.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// session mirrors the shape of labels.Session in
// packages/pa-monitor/internal/labels/labels.go. Kept as a local struct
// (rather than importing the pa-monitor module) so this package stays a
// standalone go.mod with no internal deps on pa-monitor — both packages
// reference the wire format, not Go types.
type session struct {
	ID    string            `json:"ID"`
	PID   int               `json:"PID"`
	CWD   string            `json:"CWD"`
	Env   map[string]string `json:"Env"`
	Model string            `json:"Model"`
}

// output is the JSON the pa-monitor daemon expects on our stdout:
// `{"labels": {key: value, ...}}`. An empty labels object is valid; it means
// "no decoration to add". The daemon merges this on top of the built-in
// detector labels (argument-wins semantics).
type output struct {
	Labels map[string]string `json:"labels"`
}

// scopeLabelKey is the single label key this decorator produces.
const scopeLabelKey = "workspace.scope"

// envRules is the fallback source of rules when no -rule flags are given.
// Value format: `PREFIX=SCOPE` entries separated by ';'.
const envRules = "PA_MONITOR_SCOPE_RULES"

// rule maps a CWD path prefix to a workspace.scope value.
type rule struct {
	prefix string
	scope  string
}

// run reads a session JSON document from r, maps its CWD to a scope using the
// rules loaded from the -rule flags (os.Args) and the PA_MONITOR_SCOPE_RULES
// env var, and writes the JSON-encoded output to w. Factored out of main so
// tests can call it directly without shelling out.
//
// Contract (per pa-monitor's decorator runner):
//   - Must read all of stdin before writing.
//   - Must write a single JSON object on stdout.
//   - Non-zero exit / parse error / timeout -> daemon swallows our output.
func run(r io.Reader, w io.Writer) error {
	return runWith(r, w, loadRules(os.Args[1:], os.Getenv(envRules)))
}

// runWith is run with the rule set already resolved — the seam tests use to
// exercise the read->decorate->write path without touching flags/env.
func runWith(r io.Reader, w io.Writer, rules []rule) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	var s session
	// Empty input is treated as an empty session — emit empty labels rather
	// than failing. The daemon may invoke us with `{}` during startup checks.
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("parse session JSON: %w", err)
		}
	}

	labels := decorate(s, rules)
	if labels == nil {
		labels = map[string]string{}
	}

	enc := json.NewEncoder(w)
	if err := enc.Encode(output{Labels: labels}); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

// decorate maps the session's CWD to a scope by LONGEST-prefix match over the
// rules. On a match it returns `{workspace.scope: <scope>}`; on no match it
// returns an empty map so the daemon's DefaultScope ("personal") stands.
// Matching is path-segment aware: a rule prefix matches when the CWD equals
// the prefix or is a child of it (CWD == prefix or CWD has "prefix/" as its
// prefix), so `/Volumes/acmeX` does NOT match rule `/Volumes/acme`.
func decorate(s session, rules []rule) map[string]string {
	scope := ""
	best := -1
	for _, r := range rules {
		p := strings.TrimRight(r.prefix, "/")
		if p == "" || r.scope == "" {
			continue
		}
		if s.CWD == p || strings.HasPrefix(s.CWD, p+"/") {
			if len(p) > best {
				best = len(p)
				scope = r.scope
			}
		}
	}
	if scope == "" {
		return map[string]string{}
	}
	return map[string]string{scopeLabelKey: scope}
}

// loadRules resolves the rule set from the -rule flags (args) and the env
// fallback. env is parsed first (fallback), then flags override on a duplicate
// prefix. First-seen order is preserved for determinism.
func loadRules(args []string, env string) []rule {
	byPrefix := map[string]string{}
	order := make([]string, 0)
	add := func(prefix, scope string) {
		if _, seen := byPrefix[prefix]; !seen {
			order = append(order, prefix)
		}
		byPrefix[prefix] = scope
	}
	for _, e := range strings.Split(env, ";") {
		if p, sc, ok := parseRuleEntry(e); ok {
			add(p, sc)
		}
	}
	for _, e := range parseRuleFlagValues(args) {
		if p, sc, ok := parseRuleEntry(e); ok {
			add(p, sc)
		}
	}
	out := make([]rule, 0, len(order))
	for _, p := range order {
		out = append(out, rule{prefix: p, scope: byPrefix[p]})
	}
	return out
}

// parseRuleEntry parses a single `PREFIX=SCOPE` entry. Surrounding whitespace
// is trimmed. An entry without a '=', or with an empty prefix or scope, is
// rejected (ok=false).
func parseRuleEntry(entry string) (prefix, scope string, ok bool) {
	entry = strings.TrimSpace(entry)
	i := strings.Index(entry, "=")
	if i <= 0 {
		return "", "", false
	}
	prefix = strings.TrimSpace(entry[:i])
	scope = strings.TrimSpace(entry[i+1:])
	if prefix == "" || scope == "" {
		return "", "", false
	}
	return prefix, scope, true
}

// stringList collects repeated -rule flag values.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseRuleFlagValues extracts the raw values of a repeatable -rule flag from
// args. It uses a private FlagSet with ContinueOnError and discards output, so
// unrelated flags (e.g. `go test`'s -test.* flags on os.Args) do not abort the
// process — parsing simply stops, keeping any -rule values collected first.
func parseRuleFlagValues(args []string) []string {
	var vals stringList
	fs := flag.NewFlagSet("pa-monitor-decorator-scope", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&vals, "rule", "prefix=scope mapping; repeatable")
	_ = fs.Parse(args)
	return []string(vals)
}

func main() {
	// Per the decorator protocol the daemon always sets PA_MONITOR_DECORATE=1.
	// If invoked some other way (a user running the binary by hand, a
	// misconfigured wrapper) bail out silently — same shape as the runner's
	// swallow-and-warn so we don't pollute logs.
	if os.Getenv("PA_MONITOR_DECORATE") != "1" {
		os.Exit(0)
	}

	if err := run(os.Stdin, os.Stdout); err != nil {
		// Decorator output is advisory; the runner swallows errors. We surface
		// them on stderr for ad-hoc debugging only.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
