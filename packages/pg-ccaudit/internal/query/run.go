package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Format is an output rendering.
type Format string

const (
	FormatTable Format = "table"
	FormatTSV   Format = "tsv"
	FormatJSON  Format = "json"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatTable, FormatTSV, FormatJSON:
		return Format(s), nil
	default:
		return "", fmt.Errorf("unknown format %q (want table, tsv or json)", s)
	}
}

// Request is one canned-query invocation.
type Request struct {
	Query  Query
	Args   []string
	Since  string
	Until  string
	Format Format
}

// Result is a rendered result set.
type Result struct {
	Columns []string
	Rows    [][]any
}

// Bind resolves the request's positional args into SQL named parameters.
func (r Request) Bind() ([]any, error) {
	if len(r.Args) > len(r.Query.Params) {
		return nil, fmt.Errorf("query %s takes at most %d argument(s), got %d (usage: %s)",
			r.Query.Name, len(r.Query.Params), len(r.Args), r.Query.Usage())
	}
	out := make([]any, 0, len(r.Query.Params)+2)
	for i, p := range r.Query.Params {
		v := p.Default
		if i < len(r.Args) && r.Args[i] != "" {
			v = r.Args[i]
		}
		if p.Numeric {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("query %s: argument %s must be an integer, got %q", r.Query.Name, p.Name, v)
			}
			out = append(out, sql.Named(p.Name, n))
			continue
		}
		out = append(out, sql.Named(p.Name, v))
	}
	if r.Query.Window {
		out = append(out, sql.Named("since", r.Since), sql.Named("until", r.Until))
	}
	return out, nil
}

// Run executes a canned query against a read-only handle.
func Run(ctx context.Context, db *sql.DB, req Request) (*Result, error) {
	args, err := req.Bind()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, req.Query.SQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s v%d: %w", req.Query.Name, req.Query.Version, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("query %s: columns: %w", req.Query.Name, err)
	}
	res := &Result{Columns: cols}
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("query %s: scan: %w", req.Query.Name, err)
		}
		res.Rows = append(res.Rows, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query %s: %w", req.Query.Name, err)
	}
	return res, nil
}

// Render writes the result set.
//
// Every rendering carries the query NAME, its VERSION and the WINDOW it covered,
// so a result pasted into a review is self-describing and comparable with the
// next audit's (T-10). A table of numbers with no provenance is what made the
// previous census unrepeatable.
func Render(w io.Writer, req Request, res *Result) error {
	switch req.Format {
	case FormatJSON:
		return renderJSON(w, req, res)
	case FormatTSV:
		fmt.Fprintln(w, "# "+header(req))
		fmt.Fprintln(w, strings.Join(res.Columns, "\t"))
		for _, row := range res.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = cellString(c)
			}
			fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		return nil
	default:
		fmt.Fprintln(w, "# "+header(req))
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
		seps := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			seps[i] = strings.Repeat("-", len(c))
		}
		fmt.Fprintln(tw, strings.Join(seps, "\t"))
		for _, row := range res.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = oneLine(cellString(c))
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(w, "(%d row(s))\n", len(res.Rows))
		return nil
	}
}

func renderJSON(w io.Writer, req Request, res *Result) error {
	rows := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		m := make(map[string]any, len(res.Columns))
		for i, c := range res.Columns {
			m[c] = normalizeJSON(row[i])
		}
		rows = append(rows, m)
	}
	payload := map[string]any{
		"query":   req.Query.Name,
		"version": req.Query.Version,
		"since":   req.Since,
		"until":   req.Until,
		"args":    req.Args,
		"columns": res.Columns,
		"rows":    rows,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func header(req Request) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "pg-ccaudit query %s v%d", req.Query.Name, req.Query.Version)
	if len(req.Args) > 0 {
		fmt.Fprintf(&sb, " args=%s", strings.Join(req.Args, ","))
	}
	sb.WriteString(" window=")
	if req.Since == "" && req.Until == "" {
		sb.WriteString("all")
	} else {
		since := req.Since
		if since == "" {
			since = "-inf"
		}
		until := req.Until
		if until == "" {
			until = "+inf"
		}
		fmt.Fprintf(&sb, "[%s,%s)", since, until)
	}
	return sb.String()
}

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

func normalizeJSON(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// oneLine keeps the table rendering to one row per record. Error bodies and
// narration are multi-line by nature; tsv/json keep them intact.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}
