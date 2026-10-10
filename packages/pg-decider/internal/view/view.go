// Package view decodes the pg-desk composite view contract `pg-desk.view/v1`
// (docs/behavior/pg-desk/show.md) and reads it by exec'ing
// `pg-desk <type> show <id> --json`. It never imports packages/pg-desk or
// packages/pg-connector: the JSON is decoded with this module's own structs.
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Contract is the only view contract this module understands.
const Contract = "pg-desk.view/v1"

// View is one decoded composite view.
type View struct {
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
	Links       []Link
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

// Decorations are the stored interpretation of the entity.
type Decorations struct {
	// Relationship is "mine", "co-owned" or "team".
	Relationship string        `json:"relationship"`
	Dispositions []Disposition `json:"dispositions"`
	Urgency      string        `json:"urgency"`
	Category     string        `json:"category"`
}

// Disposition is one comment's computed disposition and its override.
type Disposition struct {
	CommentID string  `json:"comment_id"`
	Computed  string  `json:"computed"`
	Override  *string `json:"override"`
}

// Annotations are the operator/decider-set annotations of the entity.
type Annotations struct {
	Hidden struct {
		Value  bool    `json:"value"`
		Reason *string `json:"reason"`
	} `json:"hidden"`
	WIP         bool     `json:"wip"`
	Suppress    []string `json:"suppress"`
	ForceReview bool     `json:"force_review"`
	// ForceReviewSHA is annotations.force_review_sha: nil when unset.
	ForceReviewSHA *string `json:"force_review_sha"`
	// ReadyToLand is annotations.ready_to_land: nil when unset (member added
	// by pg2-2j5ac.52.14.13).
	ReadyToLand *bool `json:"ready_to_land,omitempty"`
	// FocusSelected is annotations.focus_selected, with presence tracked:
	// a member absent from the printed view (an older pg-desk, or a
	// rollback) is NOT the same as a present null. The focus rule decides
	// how a run fails closed on an absent member; the reader does not.
	FocusSelected FocusSelected `json:"focus_selected"`
	// Decider maps decider name -> key -> value.
	Decider map[string]map[string]string `json:"decider"`
}

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

// Link is one entity linked to the viewed one.
type Link struct {
	Type, ID, Relation, URL, State, Assignee string
	Labels, Origins                          []string
	// Metadata holds the linked entity's metadata; scalar values are
	// rendered as their JSON text (strings unquoted).
	Metadata map[string]string
	// Title is the linked entity's stored title, "" when none (member added
	// by pg2-2j5ac.52.14.13).
	Title string
}

// UnmarshalJSON decodes a printed link. origins[] are objects whose
// `origin` member (e.g. "derived:branch-name") becomes the string entry.
func (l *Link) UnmarshalJSON(data []byte) error {
	var w struct {
		Type     string                     `json:"type"`
		ID       string                     `json:"id"`
		Relation string                     `json:"relation"`
		URL      string                     `json:"url"`
		State    string                     `json:"state"`
		Assignee string                     `json:"assignee"`
		Title    string                     `json:"title"`
		Labels   []string                   `json:"labels"`
		Metadata map[string]json.RawMessage `json:"metadata"`
		Origins  []json.RawMessage          `json:"origins"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*l = Link{
		Type: w.Type, ID: w.ID, Relation: w.Relation, URL: w.URL, State: w.State,
		Assignee: w.Assignee, Title: w.Title, Labels: w.Labels,
	}
	if w.Metadata != nil {
		l.Metadata = make(map[string]string, len(w.Metadata))
		for k, raw := range w.Metadata {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				l.Metadata[k] = s
			} else {
				l.Metadata[k] = strings.TrimSpace(string(raw))
			}
		}
	}
	for _, raw := range w.Origins {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			l.Origins = append(l.Origins, s)
			continue
		}
		var o struct {
			Origin string `json:"origin"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("link %q origin: %w", w.ID, err)
		}
		l.Origins = append(l.Origins, o.Origin)
	}
	return nil
}

// Parse decodes one printed view. Unknown members are tolerated; a contract
// other than Contract is an error. View.Raw is data.
func Parse(data []byte) (*View, error) {
	var w struct {
		Contract    string          `json:"contract"`
		Type        string          `json:"type"`
		ID          string          `json:"id"`
		Version     int64           `json:"version"`
		AsOf        string          `json:"as_of"`
		Stale       bool            `json:"stale"`
		Snapshot    json.RawMessage `json:"snapshot"`
		Decorations Decorations     `json:"decorations"`
		Annotations Annotations     `json:"annotations"`
		Links       []Link          `json:"links"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parse view: %w", err)
	}
	if w.Contract != Contract {
		return nil, fmt.Errorf("unsupported view contract %q (want %q)", w.Contract, Contract)
	}
	v := &View{
		Raw: json.RawMessage(data), Contract: w.Contract, Type: w.Type, ID: w.ID,
		Version: w.Version, AsOf: w.AsOf, Stale: w.Stale, SnapshotRaw: w.Snapshot,
		Decorations: w.Decorations, Annotations: w.Annotations, Links: w.Links,
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
