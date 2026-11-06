// Package hookdecisions is the evidence provider for the agent-workflow lens:
// it reads the approval hook's decision database read-only and returns
// evidence rows for an explicit window and scope. The provider owns the
// subject key; per-session fields appear only as sample references.
package hookdecisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SchemaVersion is the evidence-row schema version this provider emits.
const SchemaVersion = 1

// Row is one evidence row (the evidence-row schema).
type Row struct {
	SchemaVersion int                `json:"schema_version"`
	ID            string             `json:"id"`
	SubjectKey    string             `json:"subject_key"`
	DefinedIn     string             `json:"defined_in"`
	Type          string             `json:"type"`
	Kind          string             `json:"kind"`
	Scope         string             `json:"scope"`
	Window        Window             `json:"window"`
	Metrics       map[string]float64 `json:"metrics"`
	Baseline      []BaselineWindow   `json:"baseline"`
	FirstSeen     string             `json:"first_seen"`
	LastSeen      string             `json:"last_seen"`
	Query         string             `json:"query"`
	Samples       []string           `json:"samples"`
}

// Window is the evidence window, UTC with a Z suffix.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// BaselineWindow is a comparison window; this provider emits none.
type BaselineWindow struct {
	From    string             `json:"from"`
	To      string             `json:"to"`
	Metrics map[string]float64 `json:"metrics"`
}

// query is one canned query: the decision outcome it selects. The query name
// is also the row kind.
type query struct {
	Name    string
	Outcome string
}

var queries = []query{
	{Name: "denied", Outcome: "denied"},
	{Name: "failed", Outcome: "failed"},
	{Name: "rejected", Outcome: "rejected"},
}

// QueryNames lists every query the provider defines, in run order.
func QueryNames() []string {
	out := make([]string, len(queries))
	for i, q := range queries {
		out[i] = q.Name
	}
	return out
}

// Options is one provider invocation.
type Options struct {
	DBPath string
	// Since and Until bound the window, half-open [Since, Until).
	Since, Until time.Time
	Scope        string
	// Queries selects queries by name; empty runs all of them.
	Queries []string
	// CwdPrefixes keeps only decisions whose recorded working directory is
	// under one of these (OR); ExcludeCwdPrefixes drops those under any.
	CwdPrefixes        []string
	ExcludeCwdPrefixes []string
}

// UsageError marks a caller mistake (exit 2) as opposed to a runtime failure.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// DefaultDBPath is where the approval hook keeps its decision database.
func DefaultDBPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "claude-extended-tool-approver", "asks.db")
}

// NormalizePrefixes validates scope prefixes: each must be absolute and below
// the root; trailing slashes are ignored.
func NormalizePrefixes(flagName string, in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !strings.HasPrefix(p, "/") {
			return nil, &UsageError{fmt.Sprintf("%s %q: prefix must be an absolute path", flagName, p)}
		}
		n := strings.TrimRight(p, "/")
		if n == "" {
			return nil, &UsageError{fmt.Sprintf("%s %q: prefix must name a directory below the root", flagName, p)}
		}
		out = append(out, n)
	}
	return out, nil
}

// underAny reports whether cwd equals or is below any prefix, by whole path
// components ("/a/b" matches "/a/b" and "/a/b/c", never "/a/bc").
func underAny(cwd string, prefixes []string) bool {
	for _, p := range prefixes {
		if cwd == p || strings.HasPrefix(cwd, p+"/") {
			return true
		}
	}
	return false
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func selectQueries(names []string) ([]query, error) {
	if len(names) == 0 {
		return queries, nil
	}
	var out []query
	seen := map[string]bool{}
	for _, n := range names {
		var found *query
		for i := range queries {
			if queries[i].Name == n {
				found = &queries[i]
			}
		}
		if found == nil {
			return nil, &UsageError{fmt.Sprintf("unknown query %q (known: %s)", n, strings.Join(QueryNames(), ", "))}
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, *found)
		}
	}
	return out, nil
}

// OpenReadOnly opens the decision database with the SQLite mode=ro URI.
func OpenReadOnly(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type agg struct {
	kind, subject, queryName string
	count                    int
	sessions                 map[string]bool
	first, last              string
	samples                  []sampleRef
}

type sampleRef struct {
	id      int64
	session string
}

const maxSamples = 3

// Run executes the selected queries and returns evidence rows with ids
// assigned in (kind, subject key) order. A missing window is a UsageError;
// an unreadable database is a plain error.
func Run(ctx context.Context, o Options) ([]Row, error) {
	if o.Since.IsZero() || o.Until.IsZero() {
		return nil, &UsageError{"--since and --until are required"}
	}
	if !o.Since.Before(o.Until) {
		return nil, &UsageError{"--since must be before --until"}
	}
	if o.Scope == "" {
		return nil, &UsageError{"--scope is required"}
	}
	qs, err := selectQueries(o.Queries)
	if err != nil {
		return nil, err
	}
	inc, err := NormalizePrefixes("--cwd-prefix", o.CwdPrefixes)
	if err != nil {
		return nil, err
	}
	exc, err := NormalizePrefixes("--exclude-cwd-prefix", o.ExcludeCwdPrefixes)
	if err != nil {
		return nil, err
	}

	db, err := OpenReadOnly(o.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open decision database: %w", err)
	}
	defer func() { _ = db.Close() }()

	since, until := stamp(o.Since), stamp(o.Until)
	var rows []Row
	for _, q := range qs {
		groups, err := runQuery(ctx, db, q, since, until, inc, exc)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.Name, err)
		}
		for _, g := range groups {
			samples := make([]string, 0, len(g.samples))
			for _, s := range g.samples {
				samples = append(samples, fmt.Sprintf("decision:%d session:%s", s.id, s.session))
			}
			rows = append(rows, Row{
				SchemaVersion: SchemaVersion,
				SubjectKey:    g.subject,
				DefinedIn:     "claude-extended-tool-approver",
				Type:          "tool-call",
				Kind:          g.kind,
				Scope:         o.Scope,
				Window:        Window{From: since, To: until},
				Metrics: map[string]float64{
					"count":    float64(g.count),
					"sessions": float64(len(g.sessions)),
				},
				Baseline:  []BaselineWindow{},
				FirstSeen: g.first,
				LastSeen:  g.last,
				Query:     g.queryName,
				Samples:   samples,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].SubjectKey < rows[j].SubjectKey
	})
	for i := range rows {
		rows[i].ID = fmt.Sprintf("E-%03d", i+1)
	}
	if rows == nil {
		rows = []Row{}
	}
	return rows, nil
}

func runQuery(ctx context.Context, db *sql.DB, q query, since, until string, inc, exc []string) ([]*agg, error) {
	r, err := db.QueryContext(ctx, `SELECT id, session_id, cwd, tool_name, tool_input_json, created_at
		FROM tool_decisions
		WHERE excluded = 0 AND outcome = ? AND created_at >= ? AND created_at < ?
		ORDER BY id`, q.Outcome, since, until)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	byKey := map[string]*agg{}
	for r.Next() {
		var (
			id                                     int64
			session, cwd, tool, inputJSON, created string
		)
		if err := r.Scan(&id, &session, &cwd, &tool, &inputJSON, &created); err != nil {
			return nil, err
		}
		if len(inc) > 0 && !underAny(cwd, inc) {
			continue
		}
		if len(exc) > 0 && underAny(cwd, exc) {
			continue
		}
		subject := SubjectKey(tool, inputJSON)
		g := byKey[subject]
		if g == nil {
			g = &agg{kind: q.Name, subject: subject, queryName: q.Name, sessions: map[string]bool{}, first: created, last: created}
			byKey[subject] = g
		}
		g.count++
		g.sessions[session] = true
		if created < g.first {
			g.first = created
		}
		if created > g.last {
			g.last = created
		}
		if len(g.samples) < maxSamples {
			g.samples = append(g.samples, sampleRef{id: id, session: session})
		}
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	out := make([]*agg, 0, len(byKey))
	for _, g := range byKey {
		out = append(out, g)
	}
	return out, nil
}

// subcommandTools are command-line tools whose second word names the action,
// so "git push" and "git status" are different subjects while "ls a" and
// "ls b" are one.
var subcommandTools = map[string]bool{
	"git": true, "nix": true, "bd": true, "gh": true, "go": true, "docker": true,
	"kubectl": true, "cargo": true, "npm": true, "pn": true, "pg-hooks": true,
}

// SubjectKey collapses a decision into its kind of command: for Bash the
// leading program (plus its subcommand for known multi-command tools), for any
// other tool the tool name. Per-call arguments, paths and sessions never
// enter the key.
func SubjectKey(tool, inputJSON string) string {
	if tool != "Bash" {
		return tool
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		return "Bash:(unparsed)"
	}
	words := strings.Fields(in.Command)
	i := 0
	for i < len(words) && isEnvAssign(words[i]) {
		i++
	}
	if i >= len(words) {
		return "Bash:(empty)"
	}
	prog := filepath.Base(words[i])
	if subcommandTools[prog] && i+1 < len(words) && isSubcommand(words[i+1]) {
		return "Bash:" + prog + " " + words[i+1]
	}
	return "Bash:" + prog
}

func isEnvAssign(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for _, c := range w[:eq] {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isSubcommand(w string) bool {
	if w == "" || w[0] < 'a' || w[0] > 'z' {
		return false
	}
	for _, c := range w {
		if !(c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

var _ = errors.Is
