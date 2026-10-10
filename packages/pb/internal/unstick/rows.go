// Package unstick holds the deterministic stages of the /pb:unstick-beads
// sweep: export-row decoding (rows.go), the sweep-marker grammar (marker.go)
// and work-directory allocation (workdir.go). It deliberately imports neither
// internal/bd nor internal/run, so pure logic stays testable without a runner.
package unstick

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Bead status values as they appear in `bd export` rows.
const (
	StatusOpen       = "open"
	StatusBlocked    = "blocked"
	StatusInProgress = "in_progress"
	StatusDeferred   = "deferred"
	StatusClosed     = "closed"
)

// Dependency edge types the sweep acts on (the export also carries
// discovered-from, related, relates-to, duplicates and supersedes, which the
// sweep ignores).
const (
	DepBlocks      = "blocks"
	DepParentChild = "parent-child"
)

// Dep is one entry of a row's dependencies[]. Direction: IssueID depends on
// DependsOnID. For DepParentChild, IssueID is the CHILD and DependsOnID the
// PARENT. Metadata is a JSON-encoded STRING (not an object), kept verbatim.
type Dep struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	CreatedAt   string `json:"created_at,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	Metadata    string `json:"metadata,omitempty"`
}

// Comment is one entry of a row's comments[]. ID is kept as text because bd
// emits it as a number; decoding accepts either a number or a string.
type Comment struct {
	ID        string `json:"id,omitempty"`
	IssueID   string `json:"issue_id,omitempty"`
	Author    string `json:"author,omitempty"`
	Text      string `json:"text,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// UnmarshalJSON decodes a comment, tolerating a numeric or string id and
// explicit nulls.
func (c *Comment) UnmarshalJSON(b []byte) error {
	var aux struct {
		ID        json.RawMessage `json:"id"`
		IssueID   string          `json:"issue_id"`
		Author    string          `json:"author"`
		Text      string          `json:"text"`
		CreatedAt string          `json:"created_at"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	id := strings.TrimSpace(string(aux.ID))
	if id == "null" {
		id = ""
	} else if strings.HasPrefix(id, `"`) {
		var s string
		if err := json.Unmarshal(aux.ID, &s); err != nil {
			return err
		}
		id = s
	}
	*c = Comment{ID: id, IssueID: aux.IssueID, Author: aux.Author, Text: aux.Text, CreatedAt: aux.CreatedAt}
	return nil
}

// Row is one decoded `bd export` line (one bead). Absent fields and explicit
// JSON nulls (assignee, labels, defer_until) both decode to the zero value.
// Unknown extra fields are ignored. Timestamps are RFC3339 strings; use
// ParseTime to read them.
type Row struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title,omitempty"`
	Description        string    `json:"description,omitempty"`
	Status             string    `json:"status,omitempty"`
	Priority           int       `json:"priority"`
	IssueType          string    `json:"issue_type,omitempty"`
	Labels             []string  `json:"labels,omitempty"`
	Notes              string    `json:"notes,omitempty"`
	DeferUntil         string    `json:"defer_until,omitempty"`
	AcceptanceCriteria string    `json:"acceptance_criteria,omitempty"`
	Assignee           string    `json:"assignee,omitempty"`
	CreatedAt          string    `json:"created_at,omitempty"`
	UpdatedAt          string    `json:"updated_at,omitempty"`
	ClosedAt           string    `json:"closed_at,omitempty"`
	CloseReason        string    `json:"close_reason,omitempty"`
	Dependencies       []Dep     `json:"dependencies,omitempty"`
	Comments           []Comment `json:"comments,omitempty"`
}

// HasLabel reports whether the row carries label.
func (r Row) HasLabel(label string) bool {
	for _, l := range r.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// ReadyRow is the subset of a `bd ready --json` row the sweep needs.
// IsTemplate appears only when true; absence decodes to false.
type ReadyRow struct {
	ID         string   `json:"id"`
	Status     string   `json:"status,omitempty"`
	IssueType  string   `json:"issue_type,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Assignee   string   `json:"assignee,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	IsTemplate bool     `json:"is_template,omitempty"`
}

// ParseReady decodes `bd ready --json` output: the {data, schema_version}
// envelope, or (tolerated) a bare array. An envelope whose data key is absent
// or null is an error (positive control), so a malformed reply is not
// mistaken for an empty queue.
func ParseReady(data []byte) ([]ReadyRow, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("bd ready: empty output")
	}
	if trimmed[0] == '[' {
		var rows []ReadyRow
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("parse bd ready json: %w", err)
		}
		return rows, nil
	}
	var env struct {
		Data *[]ReadyRow `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, fmt.Errorf("parse bd ready json: %w", err)
	}
	if env.Data == nil {
		return nil, fmt.Errorf("bd ready returned no data envelope (positive control failed): %s", truncate(string(trimmed), 200))
	}
	return *env.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ReadExport decodes a `bd export` JSONL stream. Blank lines are skipped, rows
// whose _type is present and not "issue" are skipped, and a row without an id
// is an error. Errors carry the 1-based line number. Order is preserved.
func ReadExport(r io.Reader) ([]Row, error) {
	br := bufio.NewReader(r)
	var rows []Row
	for line := 1; ; line++ {
		b, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			var probe struct {
				Type string `json:"_type"`
			}
			if jerr := json.Unmarshal(b, &probe); jerr != nil {
				return nil, fmt.Errorf("export line %d: %w", line, jerr)
			}
			if probe.Type == "" || probe.Type == "issue" {
				var row Row
				if jerr := json.Unmarshal(b, &row); jerr != nil {
					return nil, fmt.Errorf("export line %d: %w", line, jerr)
				}
				if row.ID == "" {
					return nil, fmt.Errorf("export line %d: row has no id", line)
				}
				rows = append(rows, row)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return rows, nil
			}
			return nil, fmt.Errorf("read export: %w", err)
		}
	}
}

// ReadExportFile opens path and decodes it with ReadExport.
func ReadExportFile(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := ReadExport(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rows, nil
}

// ParseTime parses an RFC3339 timestamp (with or without fractional seconds)
// and returns it in UTC. An empty string is an error.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// FormatTime renders t as UTC Z-form RFC3339 (seconds precision), the form all
// sweep files and markers use.
func FormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// Index maps id to the row's position in rows. A duplicate id keeps the first.
func Index(rows []Row) map[string]int {
	m := make(map[string]int, len(rows))
	for i, r := range rows {
		if _, dup := m[r.ID]; !dup {
			m[r.ID] = i
		}
	}
	return m
}
