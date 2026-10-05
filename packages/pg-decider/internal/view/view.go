// Package view decodes the pg-desk composite view contract `pg-desk.view/v1`
// (docs/behavior/pg-desk/show.md) and reads it by exec'ing
// `pg-desk <type> show <id> --json`. It never imports packages/pg-desk or
// packages/pg-connector: the JSON is decoded with this module's own structs.
package view

import (
	"encoding/json"
	"fmt"
	"strings"
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
	// ReadyToLand is annotations.ready_to_land: nil when unset (member added
	// by pg2-2j5ac.52.14.13).
	ReadyToLand *bool `json:"ready_to_land,omitempty"`
	// Decider maps decider name -> key -> value.
	Decider map[string]map[string]string `json:"decider"`
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
	return v, nil
}
