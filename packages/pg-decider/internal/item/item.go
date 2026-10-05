// Package item reads the routed pg-router item handed to `apply --from-item`
// (entity-change-flow design 9.4). apply always re-reads the view and never
// decides from the item's kind, so this type carries identification only.
package item

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// stdin is what Read("-") consumes; swapped by tests.
var stdin io.Reader = os.Stdin

// Routed is the routed item: {id, type "<type>.<kind>", title, metadata{...}}.
type Routed struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Metadata struct {
		EntityType      string   `json:"entity_type"`
		EntityID        string   `json:"entity_id"`
		Kind            string   `json:"kind"`
		Origin          string   `json:"origin"`
		Seq             int64    `json:"seq"`
		Version         int64    `json:"version"`
		DegradedSources []string `json:"degraded_sources"`
	} `json:"metadata"`
}

// Read decodes a routed item from path; path "-" reads stdin. Unknown
// members and an absent metadata object are tolerated.
func Read(path string) (*Routed, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read routed item %q: %w", path, err)
	}
	var r Routed
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse routed item %q: %w", path, err)
	}
	return &r, nil
}
