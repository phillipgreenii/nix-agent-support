// Package view decodes the pg-desk composite view contract `pg-desk.view/v1`
// (docs/behavior/pg-desk/show.md) and reads it by exec'ing
// `pg-desk <type> show <id> --json`. The wire shape is the published contract
// package packages/pg-desk/pkg/deskapi; the module never imports the rest of
// packages/pg-desk or packages/pg-connector (the PR and issue snapshots are
// decoded with this module's own structs).
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
)

// Contract is the only view contract this module understands.
const Contract = deskapi.ViewContract

// DeskView is one decoded composite view: the deskapi wire members plus the
// decoded snapshots and the presence-tracked focus_selected member.
type DeskView struct {
	// Raw is the complete printed JSON, so later packets can decode members
	// added after this one landed.
	Raw                json.RawMessage
	Contract, Type, ID string
	Version            int64
	AsOf               string
	Stale              bool
	// Snapshot is the decoded PR snapshot (type "pr" only; zero otherwise).
	Snapshot PRSnapshot
	// IssueSnapshot is the decoded issue snapshot (type "issue" only; zero
	// otherwise).
	IssueSnapshot IssueSnapshot
	// SnapshotRaw is the type's snapshot exactly as printed (null when none).
	SnapshotRaw json.RawMessage
	Decorations Decorations
	Annotations Annotations
	// FocusSelected is annotations.focus_selected, with presence tracked
	// (deskapi decodes it as a pointer, which cannot tell absent from null).
	FocusSelected FocusSelected
	Links         []LinkedEntity
	// CI is the pr view's optional ci section (nil when absent).
	CI *deskapi.CI
	// ThreadChanges are the thread change records the view shows (the
	// optional `thread_changes` member, absent today: no connector feeds it
	// yet).
	ThreadChanges []ThreadChange
}

// PRSnapshot is the subset of pg-connector's schema.PR the rules read.
type PRSnapshot struct {
	ID             string    `json:"id"`
	Repo           string    `json:"repo"`
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	Branch         string    `json:"branch"`
	Base           string    `json:"base"`
	Author         string    `json:"author"`
	URL            string    `json:"url"`
	Draft          bool      `json:"draft"`
	Merged         bool      `json:"merged"`
	Labels         []string  `json:"labels,omitempty"`
	Comments       []Comment `json:"comments,omitempty"`
	HeadSHA        string    `json:"head_sha,omitempty"`
	BaseSHA        string    `json:"base_sha,omitempty"`
	Mergeable      string    `json:"mergeable,omitempty"`
	ChecksRollup   string    `json:"checks_rollup,omitempty"`
	ReviewDecision string    `json:"review_decision,omitempty"`
	NodeID         string    `json:"node_id,omitempty"`
}

// IssueSnapshot is the subset of pg-connector's schema.Issue the focus bead
// shape and the hold rules read. Unknown members are tolerated.
type IssueSnapshot struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	State          string   `json:"state"`
	URL            string   `json:"url,omitempty"`
	StatusCategory string   `json:"status_category,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	IssueType      string   `json:"issue_type,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	Assignee       string   `json:"assignee,omitempty"`
	Parent         string   `json:"parent,omitempty"`
	DueDate        string   `json:"due_date,omitempty"`
	// Metadata is the tracker's string-to-string custom fields.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Comment is one PR comment.
type Comment struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Resolved bool   `json:"resolved"`
}

// Decorations, Disposition and Annotations are the deskapi wire shapes.
type (
	Decorations = deskapi.Decorations
	Disposition = deskapi.Disposition
	Annotations = deskapi.Annotations
)

// FocusSelected is the annotations.focus_selected member decoded with
// presence. The four observable states are: absent (Present false), present
// null (Present true, Set false: unset), the string "none" (Set true, Value
// "none") and a period key (Set true, Value "YYYY-MM-DD").
type FocusSelected struct {
	// Present is true when the member appears in the printed JSON at all.
	Present bool
	// Set is true when the member is a JSON string (false for null).
	Set bool
	// Value is the string when Set, "" otherwise.
	Value string
}

// UnmarshalJSON records presence (it is only called when the member exists)
// and decodes a string or null; any other JSON type is an error.
func (f *FocusSelected) UnmarshalJSON(data []byte) error {
	*f = FocusSelected{Present: true}
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}
	if err := json.Unmarshal(data, &f.Value); err != nil {
		*f = FocusSelected{}
		return fmt.Errorf("annotations.focus_selected: want string or null: %w", err)
	}
	f.Set = true
	return nil
}

// Selected returns the period key and ok true only for a real period key: a
// string of the form YYYY-MM-DD naming a real date. Absent, null, "none",
// the empty string and a malformed key are all "not matched" (ok false).
func (f FocusSelected) Selected() (period string, ok bool) {
	if !f.Set || len(f.Value) != len("2006-01-02") {
		return "", false
	}
	if _, err := time.Parse("2006-01-02", f.Value); err != nil {
		return "", false
	}
	return f.Value, true
}

// LinkedEntity is one entity linked to the viewed one, flattened from the
// deskapi link (pointer members dereferenced, origins reduced to their names).
type LinkedEntity struct {
	Type, ID, Relation, URL, State, Assignee string
	Labels, Origins                          []string
	// Metadata holds the linked entity's metadata.
	Metadata map[string]string
	// Title is the linked entity's stored title, "" when none.
	Title string
}

func linkedEntity(l deskapi.Link) LinkedEntity {
	out := LinkedEntity{
		Type: l.Type, ID: l.ID, Relation: l.Relation, URL: l.URL, State: l.State, Title: l.Title,
	}
	if l.Assignee != nil {
		out.Assignee = *l.Assignee
	}
	if l.Labels != nil {
		out.Labels = *l.Labels
	}
	if l.Metadata != nil {
		out.Metadata = *l.Metadata
	}
	for _, o := range l.Origins {
		out.Origins = append(out.Origins, o.Origin)
	}
	return out
}

// Parse decodes one printed view. Unknown members are tolerated; a contract
// other than Contract is an error. View.Raw is data.
func Parse(data []byte) (*DeskView, error) {
	var w deskapi.View
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parse view: %w", err)
	}
	if w.Contract != Contract {
		return nil, fmt.Errorf("unsupported view contract %q (want %q)", w.Contract, Contract)
	}
	// Members deskapi does not carry: focus_selected with presence, and the
	// optional thread_changes.
	var extra struct {
		Annotations struct {
			FocusSelected FocusSelected `json:"focus_selected"`
		} `json:"annotations"`
		ThreadChanges []ThreadChange `json:"thread_changes"`
	}
	if err := json.Unmarshal(data, &extra); err != nil {
		return nil, fmt.Errorf("parse view: %w", err)
	}
	v := &DeskView{
		Raw: json.RawMessage(data), Contract: w.Contract, Type: w.Type, ID: w.ID,
		Version: w.Version, AsOf: w.AsOf, Stale: w.Stale, SnapshotRaw: w.Snapshot,
		Decorations: w.Decorations, Annotations: w.Annotations, CI: w.CI,
		FocusSelected: extra.Annotations.FocusSelected, ThreadChanges: extra.ThreadChanges,
	}
	for _, l := range w.Links {
		v.Links = append(v.Links, linkedEntity(l))
	}
	if w.Type == "pr" && len(w.Snapshot) > 0 && string(w.Snapshot) != "null" {
		if err := json.Unmarshal(w.Snapshot, &v.Snapshot); err != nil {
			return nil, fmt.Errorf("parse view snapshot: %w", err)
		}
	}
	if w.Type == "issue" && len(w.Snapshot) > 0 && string(w.Snapshot) != "null" {
		if err := json.Unmarshal(w.Snapshot, &v.IssueSnapshot); err != nil {
			return nil, fmt.Errorf("parse view issue snapshot: %w", err)
		}
	}
	return v, nil
}
