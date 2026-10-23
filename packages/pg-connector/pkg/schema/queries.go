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
	"encoding/json"
	"fmt"
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

// QueryDef is one configured query: its native expressions and whether
// its members are watched.
type QueryDef struct {
	Expr    QueryExpr
	Watched bool
}

// UnmarshalJSON accepts exactly three forms:
//
//	"a native expression"                         (watched false)
//	["expression one", "expression two"]          (watched false)
//	{"expr": "one" | ["one", "two"], "watched": true}
//
// An object with a key other than "expr" and "watched", an object with no
// "expr", and a "watched" that is not a JSON boolean are errors. "watched"
// defaults to false. JSON null keeps today's result, one empty expression
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
		if k != "expr" && k != "watched" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("query definition: unknown key %q (only \"expr\" and \"watched\" are accepted)", strings.Join(unknown, `", "`))
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
	*q = def
	return nil
}

// MarshalJSON writes the bare list form for an unwatched definition and the
// object form otherwise, so a config round-trips and an unwatched
// deployment's rendered file does not change.
func (q QueryDef) MarshalJSON() ([]byte, error) {
	if !q.Watched {
		return json.Marshal(q.Expr)
	}
	return json.Marshal(struct {
		Expr    QueryExpr `json:"expr"`
		Watched bool      `json:"watched"`
	}{q.Expr, q.Watched})
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
