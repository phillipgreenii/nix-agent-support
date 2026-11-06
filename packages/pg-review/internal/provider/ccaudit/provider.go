// Package ccaudit is the evidence provider over the canned pg-ccaudit queries.
// It runs each query as structured JSON (never the tab form, which cannot
// carry a multi-line signature containing a tab), maps rows to evidence rows
// and returns them in a fixed order with ids E-001 onward.
package ccaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"
)

// AllQueries lists the seven canned queries in the fixed order used to pick
// a merged row's primary query.
var AllQueries = []string{
	"error-rate-by-tool",
	"top-signatures",
	"cost-by-signature",
	"retry-chains",
	"denied-tool-calls",
	"interruptions",
	"hook-refusals-in-body",
}

// ErrUsage marks a caller mistake (bad flag value); the command exits 2.
var ErrUsage = errors.New("usage error")

// Exec runs `pg-ccaudit <args...>` and returns its stdout.
type Exec func(ctx context.Context, args []string) ([]byte, error)

// Options is one provider invocation.
type Options struct {
	Since, Until       string // RFC3339 with an offset (or Z); normalised to UTC Z
	Scope              string
	Queries            []string // empty means all seven
	CwdPrefixes        []string
	ExcludeCwdPrefixes []string
	Exec               Exec // nil runs pg-ccaudit from PATH
}

// Row is one evidence row; the field order follows the pinned schema.
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
	Baseline      []any              `json:"baseline"`
	FirstSeen     string             `json:"first_seen"`
	LastSeen      string             `json:"last_seen"`
	Query         string             `json:"query"`
	Samples       []string           `json:"samples"`
}

// Window is the evidence window.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

const boundLayout = "2006-01-02T15:04:05.999999999Z07:00"

// NormalizeBound parses an RFC3339 bound with an explicit offset and renders
// it in UTC with a Z suffix, keeping fractional seconds. pg-ccaudit compares
// ISO-8601 text, so an offset-bearing bound would compare wrongly.
func NormalizeBound(s string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", fmt.Errorf("%w: window bound %q is not RFC3339 with an offset", ErrUsage, s)
	}
	return t.UTC().Format(boundLayout), nil
}

func execPgCcaudit(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg-ccaudit", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg-ccaudit %v: %w: %s", args, err, bytes.TrimSpace(errb.Bytes()))
	}
	return out.Bytes(), nil
}

type payload struct {
	Query string           `json:"query"`
	Rows  []map[string]any `json:"rows"`
}

// Rows runs the selected queries and returns the evidence rows. Any query
// failure fails the whole call.
func Rows(ctx context.Context, o Options) ([]Row, error) {
	from, err := NormalizeBound(o.Since)
	if err != nil {
		return nil, err
	}
	to, err := NormalizeBound(o.Until)
	if err != nil {
		return nil, err
	}
	if parseTime(to).Before(parseTime(from)) {
		return nil, fmt.Errorf("%w: --until is before --since", ErrUsage)
	}
	if o.Scope == "" {
		return nil, fmt.Errorf("%w: --scope is required", ErrUsage)
	}
	queries := o.Queries
	if len(queries) == 0 {
		queries = AllQueries
	}
	selected := map[string]bool{}
	for _, q := range queries {
		if queryIndex(q) < 0 {
			return nil, fmt.Errorf("%w: unknown query %q", ErrUsage, q)
		}
		selected[q] = true
	}
	run := o.Exec
	if run == nil {
		run = execPgCcaudit
	}
	b := newBoard(o.Scope, from, to)
	for _, q := range AllQueries { // fixed order, independent of flag order
		if !selected[q] {
			continue
		}
		args := []string{"query", q, "--since", from, "--until", to, "--format", "json", "--no-staleness"}
		for _, p := range o.CwdPrefixes {
			args = append(args, "--cwd-prefix", p)
		}
		for _, p := range o.ExcludeCwdPrefixes {
			args = append(args, "--exclude-cwd-prefix", p)
		}
		raw, err := run(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q, err)
		}
		var p payload
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("query %s: decode output: %w", q, err)
		}
		b.ingest(q, p.Rows)
	}
	b.finish(selected["top-signatures"])
	return b.rows(), nil
}

func queryIndex(q string) int {
	for i, n := range AllQueries {
		if n == q {
			return i
		}
	}
	return -1
}

// Encode writes the rows as one indented JSON array.
func Encode(w io.Writer, rows []Row) error {
	if rows == nil {
		rows = []Row{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

// Run is Rows followed by Encode; nothing is written on failure.
func Run(ctx context.Context, o Options, w io.Writer) error {
	rows, err := Rows(ctx, o)
	if err != nil {
		return err
	}
	return Encode(w, rows)
}

func sortRows(rs []Row) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Kind != rs[j].Kind {
			return rs[i].Kind < rs[j].Kind
		}
		return rs[i].SubjectKey < rs[j].SubjectKey
	})
}
