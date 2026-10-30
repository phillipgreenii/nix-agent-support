// queries.go: the {"queries": {...}} shape a list-implementing backend's
// own per-backend config block carries (bead pg2-2j5ac.28.1) — a
// caller-facing query NAME resolved to one or more
// backend-native query expressions, read from the request's own opaque
// Config member (pkg/scriptout.ConfigFromContext), never from a file the
// backend reads itself (statelessness).
//
// This lives in pkg/schema, not pkg/scriptout: "queries" is a convention
// shared by the pr and issue capabilities' own "list" op, not a property
// of the wire envelope itself (pkg/scriptout stays capability-agnostic —
// see its own package doc comment), and pkg/schema is already the shared,
// cross-backend-boundary surface pr/issue's dispatch tables import
// [freedom boundary: "exact ... internal helper names ... are the
// implementer's choice"].
package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// QueryExpr is one caller-facing query name's own resolved definition: one
// or more backend-native query expressions. The wire config value MAY be
// given as either a single string or a list of strings (design: "A
// query value MAY be a list of expressions; the backend runs each, unions
// results deduplicated by id, and reports truncated if any member
// truncated") — UnmarshalJSON normalizes both forms into a []string, so
// every caller ranges over query definitions uniformly regardless of which
// form the config author wrote.
type QueryExpr []string

// UnmarshalJSON implements the single-string-or-list-of-strings
// normalization described on QueryExpr's own doc comment.
func (q *QueryExpr) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*q = QueryExpr{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*q = QueryExpr(multi)
	return nil
}

// QueryDef is one configured query: its native expressions, whether its
// members are watched, and the parameters its expressions take. Params maps a
// parameter name to the regular expression every value MUST fully match
// (bead pg2-eclw3j.3); the expressions carry {name} placeholders. Only the
// backend's own config declares names, expressions and patterns: a request
// supplies values only (INV-ERR-3, INV-STATE-1). A query with Params MUST NOT
// be watched, because a watched query's ledger is keyed by query name alone.
type QueryDef struct {
	Expr    QueryExpr
	Watched bool
	Params  map[string]string
}

// paramNamePattern is the closed shape of a declared parameter name, which
// keeps {name} placeholders unambiguous in an expression.
var paramNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// placeholderPattern finds every {...} span in an expression token.
var placeholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

// UnmarshalJSON accepts exactly three forms:
//
//	"a native expression"                         (watched false)
//	["expression one", "expression two"]          (watched false)
//	{"expr": "one" | ["one", "two"], "watched": true, "params": {"name": "regex"}}
//
// An object with a key other than "expr", "watched" and "params", an object
// with no "expr", and a "watched" that is not a JSON boolean are errors.
// "watched" defaults to false. "params" maps a parameter name (lowercase
// letters, digits and underscores, starting with a letter) to a regular
// expression that compiles; a watched query with params is an error. JSON null keeps today's result, one empty expression
// (QueryExpr{""}: unmarshalling null into a string is a no-op), unwatched; a
// test pins it, because turning it into an error would make ResolveQuery treat
// the whole queries block as absent.
func (q *QueryDef) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		var expr QueryExpr
		if err := expr.UnmarshalJSON(data); err != nil {
			return err
		}
		*q = QueryDef{Expr: expr}
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	var unknown []string
	for k := range obj {
		if k != "expr" && k != "watched" && k != "params" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("query definition: unknown key %q (only \"expr\", \"watched\" and \"params\" are accepted)", strings.Join(unknown, `", "`))
	}
	rawExpr, ok := obj["expr"]
	if !ok {
		return fmt.Errorf(`query definition: object has no "expr"`)
	}
	var def QueryDef
	if err := def.Expr.UnmarshalJSON(rawExpr); err != nil {
		return fmt.Errorf(`query definition: "expr" must be a string or a list of strings: %w`, err)
	}
	if rawWatched, ok := obj["watched"]; ok {
		if err := json.Unmarshal(rawWatched, &def.Watched); err != nil || bytes.Equal(bytes.TrimSpace(rawWatched), []byte("null")) {
			return fmt.Errorf(`query definition: "watched" must be a JSON boolean`)
		}
	}
	if rawParams, ok := obj["params"]; ok {
		var params map[string]string
		if err := json.Unmarshal(rawParams, &params); err != nil || bytes.Equal(bytes.TrimSpace(rawParams), []byte("null")) {
			return fmt.Errorf(`query definition: "params" must be an object of parameter name to regular expression`)
		}
		for name, pattern := range params {
			if !paramNamePattern.MatchString(name) {
				return fmt.Errorf("query definition: parameter name %q must match %s", name, paramNamePattern)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("query definition: parameter %q pattern %q does not compile: %v", name, pattern, err)
			}
		}
		if len(params) > 0 {
			def.Params = params
		}
	}
	if def.Watched && len(def.Params) > 0 {
		return fmt.Errorf(`query definition: a query with "params" MUST NOT be watched (its ledger is keyed by query name only)`)
	}
	*q = def
	return nil
}

// MarshalJSON writes the bare list form for an unwatched, unparameterized
// definition and the object form otherwise, so a config round-trips and an
// unparameterized deployment's rendered file does not change.
func (q QueryDef) MarshalJSON() ([]byte, error) {
	if !q.Watched && len(q.Params) == 0 {
		return json.Marshal(q.Expr)
	}
	return json.Marshal(struct {
		Expr    QueryExpr         `json:"expr"`
		Watched bool              `json:"watched,omitempty"`
		Params  map[string]string `json:"params,omitempty"`
	}{q.Expr, q.Watched, q.Params})
}

// QueriesConfig is the {"queries": {...}} shape itself, decoded straight
// from a Request's own opaque Config member.
type QueriesConfig struct {
	Queries map[string]QueryDef `json:"queries"`
}

// ResolveQuery looks up name in config's own "queries" block. config is
// the raw, opaque per-backend config JSON a Request carries (nil/empty
// when the backend has no registered config block at all) — a config with
// no matching name, or no queries block at all, resolves to (nil, false):
// a backend MUST NOT ship any built-in query names of its own (design
// ,'s "list's dispatch entry" note), so there is no compiled-in
// fallback here for either dispatch.go call site (pr, issue) that uses
// this. A malformed config (fails to even decode as an object) is treated
// the same as "no queries block" rather than surfaced as a distinct
// decode error — both dispatch.go call sites report the SAME
// query_not_recognized outcome either way, per design ("A backend
// MUST NOT treat an unrecognized name as a usage error, crash, or empty
// result").
func ResolveQuery(config json.RawMessage, name string) (QueryExpr, bool) {
	def, ok := ResolveQueryDef(config, name)
	if !ok {
		return nil, false
	}
	return def.Expr, true
}

// ResolveQueryDef returns the whole definition of name in config's own
// "queries" block, with the same absent/malformed handling as ResolveQuery.
func ResolveQueryDef(config json.RawMessage, name string) (QueryDef, bool) {
	if len(config) == 0 {
		return QueryDef{}, false
	}
	var cfg QueriesConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return QueryDef{}, false
	}
	def, ok := cfg.Queries[name]
	return def, ok
}

// CheckParams validates the request's parameter values against the query's
// declared parameters: an unknown name, a declared name with no value, and a
// value that does not fully match its declared pattern (Go's $ is end of
// text, and the pattern is anchored here) are each an error naming the
// parameter (and, for a mismatch, the pattern). Callers wrap the error as
// invalid_argument. A query with no declared params accepts no values.
func (q QueryDef) CheckParams(values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := q.Params[name]; !ok {
			return fmt.Errorf("param %q is not declared by this query", name)
		}
	}
	declared := make([]string, 0, len(q.Params))
	for name := range q.Params {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	for _, name := range declared {
		v, ok := values[name]
		if !ok {
			return fmt.Errorf("param %q is required (pattern %s)", name, q.Params[name])
		}
		re, err := regexp.Compile(`\A(?:` + q.Params[name] + `)\z`)
		if err != nil {
			return fmt.Errorf("param %q pattern %q does not compile: %v", name, q.Params[name], err)
		}
		if !re.MatchString(v) {
			return fmt.Errorf("param %q value %q does not match %s", name, v, q.Params[name])
		}
	}
	return nil
}

// SubstituteParams replaces every {name} placeholder in argv, an expression
// ALREADY split into argv tokens, with the validated value of name. Because it
// runs after the split, a value is part of exactly one token and cannot add or
// remove a token or a flag. Replacement is a single pass (a value that itself
// contains braces is never expanded again). It refuses a placeholder naming an
// undeclared parameter (any {...} left unreplaced), and a token that began
// with something other than "-" but starts with "-" after substitution (a
// value would otherwise become a flag). The result is a fresh slice; argv is
// not modified. values MUST already have passed CheckParams.
func (q QueryDef) SubstituteParams(argv []string, values map[string]string) ([]string, error) {
	out := make([]string, len(argv))
	for i, tok := range argv {
		var bad error
		sub := placeholderPattern.ReplaceAllStringFunc(tok, func(ph string) string {
			name := ph[1 : len(ph)-1]
			if _, declared := q.Params[name]; !declared {
				if bad == nil {
					bad = fmt.Errorf("expression token %q has placeholder %s that names no declared param", tok, ph)
				}
				return ph
			}
			v, ok := values[name]
			if !ok && bad == nil {
				bad = fmt.Errorf("param %q is required", name)
			}
			return v
		})
		if bad != nil {
			return nil, bad
		}
		if sub != tok && strings.HasPrefix(sub, "-") && !strings.HasPrefix(tok, "-") {
			return nil, fmt.Errorf("expression token %q would start with \"-\" after substitution", tok)
		}
		out[i] = sub
	}
	return out, nil
}

// QueryParams is what a list handler hands a backend through the context:
// the resolved query definition and the request's parameter values.
type QueryParams struct {
	Def    QueryDef
	Values map[string]string
}

type queryParamsKey struct{}

// WithQueryParams returns ctx carrying p for QueryParamsFromContext.
func WithQueryParams(ctx context.Context, p QueryParams) context.Context {
	return context.WithValue(ctx, queryParamsKey{}, p)
}

// QueryParamsFromContext returns the QueryParams a list handler attached, or
// the zero value (no declared params, no values) when none was.
func QueryParamsFromContext(ctx context.Context) QueryParams {
	p, _ := ctx.Value(queryParamsKey{}).(QueryParams)
	return p
}
