package unstick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// StatusUnknown is the status of a Ref whose bead is absent from the export.
const StatusUnknown = "UNKNOWN"

// Ref is the compact neighbour view a batch worker sees for a blocker,
// blocked dependent, parent or child.
type Ref struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Labels     []string `json:"labels"`
	DeferUntil string   `json:"defer_until"`
	ClosedAt   string   `json:"closed_at"`
}

// Fact is one bead's facts file entry: the explicit projection of the export
// row (defaults applied, so no field is ever absent or null) plus neighbour
// refs. Raw dependencies and metadata are deliberately NOT included.
type Fact struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	Status             string    `json:"status"`
	Priority           int       `json:"priority"`
	IssueType          string    `json:"issue_type"`
	Labels             []string  `json:"labels"`
	Notes              string    `json:"notes"`
	DeferUntil         string    `json:"defer_until"`
	AcceptanceCriteria string    `json:"acceptance_criteria"`
	Assignee           string    `json:"assignee"`
	Comments           []Comment `json:"comments"`
	CreatedAt          string    `json:"created_at"`
	UpdatedAt          string    `json:"updated_at"`
	ClosedAt           string    `json:"closed_at"`
	CloseReason        string    `json:"close_reason"`

	Blockers          []Ref `json:"blockers"`
	BlockedDependents []Ref `json:"blocked_dependents"`
	Parent            *Ref  `json:"parent"`
	Children          []Ref `json:"children"`
}

// MakeRef builds the Ref for id; an id absent from the export gets status
// UNKNOWN and empty fields.
func MakeRef(g *Graph, id string) Ref {
	r, ok := g.Row(id)
	if !ok {
		return Ref{ID: id, Status: StatusUnknown, Labels: []string{}}
	}
	return Ref{
		ID: r.ID, Title: r.Title, Status: r.Status,
		Labels:     nonNilStrings(r.Labels),
		DeferUntil: r.DeferUntil, ClosedAt: r.ClosedAt,
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string(nil), s...)
}

func refs(g *Graph, ids []string) []Ref {
	out := make([]Ref, 0, len(ids))
	for _, id := range ids {
		out = append(out, MakeRef(g, id))
	}
	return out
}

// MakeFact projects one bead. ok is false when id is not in the export.
func MakeFact(g *Graph, id string) (Fact, bool) {
	r, ok := g.Row(id)
	if !ok {
		return Fact{}, false
	}
	comments := append([]Comment{}, r.Comments...)
	f := Fact{
		ID: r.ID, Title: r.Title, Description: r.Description, Status: r.Status,
		Priority: r.Priority, IssueType: r.IssueType, Labels: nonNilStrings(r.Labels),
		Notes: r.Notes, DeferUntil: r.DeferUntil, AcceptanceCriteria: r.AcceptanceCriteria,
		Assignee: r.Assignee, Comments: comments, CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt, CloseReason: r.CloseReason,
		Blockers:          refs(g, g.Blockers(id)),
		BlockedDependents: refs(g, g.BlockerDependents(id)),
		Children:          refs(g, g.Children(id)),
	}
	if p, has := g.Parent(id); has {
		pr := MakeRef(g, p)
		f.Parent = &pr
	}
	return f, true
}

// Facts projects every id of batch, sorted by id. Ids absent from the export
// are skipped (they cannot be reviewed); use MissingIDs to detect them.
func Facts(batch []string, g *Graph) []Fact {
	ids := append([]string(nil), batch...)
	sort.Strings(ids)
	out := make([]Fact, 0, len(ids))
	for _, id := range ids {
		if f, ok := MakeFact(g, id); ok {
			out = append(out, f)
		}
	}
	return out
}

// MissingIDs returns the ids of batch that are absent from the export, sorted.
func MissingIDs(batch []string, g *Graph) []string {
	var out []string
	for _, id := range batch {
		if !g.Has(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// MarshalFacts renders facts as indented JSON with a trailing newline.
// Deterministic: same input, same bytes (no HTML escaping).
func MarshalFacts(facts []Fact) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(facts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteBatch writes <workdir>/batches/<name> (one sorted id per line) and
// <workdir>/facts/<name>.json for ids.
func WriteBatch(w Workdir, name string, ids []string, g *Graph) error {
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return fmt.Errorf("invalid batch name %q", name)
	}
	if miss := MissingIDs(ids, g); len(miss) > 0 {
		return fmt.Errorf("batch %s: ids not in export: %s", name, strings.Join(miss, ","))
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	list := strings.Join(sorted, "\n") + "\n"
	if err := os.WriteFile(w.Join(BatchesDir, name), []byte(list), 0o600); err != nil {
		return err
	}
	data, err := MarshalFacts(Facts(sorted, g))
	if err != nil {
		return err
	}
	return os.WriteFile(w.Join(FactsDir, name+".json"), data, 0o600)
}

// WriteBatches writes every batch as B01, B02, ... and returns the names.
func WriteBatches(w Workdir, batches [][]string, g *Graph) ([]string, error) {
	names := make([]string, 0, len(batches))
	for i, b := range batches {
		name := BatchName(i)
		if err := WriteBatch(w, name, b, g); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}
