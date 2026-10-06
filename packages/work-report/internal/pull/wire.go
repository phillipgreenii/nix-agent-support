package pull

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// This file holds work-report's own decoding of the wire shape of
// `pg-connector activity list --output json` (the umbrella verb's output): the
// standard fan-out sources[] rows plus a flat items list, each item carrying
// its source and an ActivityItem. Nothing here imports pg-connector.

// wireRow is one sources[] row.
type wireRow struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason"`
}

// wireItem is one items[] element; Item is decoded (and may be rejected) per
// item, so one bad item never costs the rest.
type wireItem struct {
	Source string          `json:"source"`
	Item   json.RawMessage `json:"item"`
}

type wireDoc struct {
	Sources []wireRow  `json:"sources"`
	Items   []wireItem `json:"items"`
}

// decodeDoc decodes stdout as the activity list document. ok is false when
// stdout is not a JSON object carrying a sources array: such output is "no
// decodable document" whatever the exit code was.
func decodeDoc(stdout []byte) (wireDoc, bool) {
	var d wireDoc
	if err := json.Unmarshal(stdout, &d); err != nil || d.Sources == nil {
		return wireDoc{}, false
	}
	return d, true
}

// wireActivity is the ActivityItem envelope.
type wireActivity struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	OccurredAt  string          `json:"occurred_at"`
	Approximate bool            `json:"approximate"`
	Summary     string          `json:"summary"`
	URL         string          `json:"url"`
	Labels      []string        `json:"labels"`
	Fields      json.RawMessage `json:"fields"`
	AsOf        string          `json:"as_of"`
	Stale       bool            `json:"stale"`
}

// toEntry validates raw's envelope and maps it to a store entry for source
// (design 7.3's mapping table). The kind-specific shape of fields is NOT
// checked: that is a recorded realization gap (INV-TYPE-1).
func toEntry(source string, extraLabels []string, raw json.RawMessage) (store.Entry, error) {
	var a wireActivity
	if err := json.Unmarshal(raw, &a); err != nil {
		return store.Entry{}, fmt.Errorf("not an activity item: %w", err)
	}
	for _, req := range []struct{ name, val string }{
		{"id", a.ID},
		{"kind", a.Kind},
		{"entity_type", a.EntityType},
		{"entity_id", a.EntityID},
		{"occurred_at", a.OccurredAt},
		{"summary", a.Summary},
	} {
		if req.val == "" {
			return store.Entry{}, fmt.Errorf("missing required field %s", req.name)
		}
	}
	occurred, err := time.Parse(time.RFC3339, a.OccurredAt)
	if err != nil {
		return store.Entry{}, fmt.Errorf("occurred_at %q is not RFC3339: %w", a.OccurredAt, err)
	}
	fields, err := mergeFields(a)
	if err != nil {
		return store.Entry{}, err
	}
	return store.Entry{
		ID:         source + ":" + a.ID,
		ExternalID: a.EntityID,
		SourceID:   source,
		Type:       a.Kind,
		OccurredAt: occurred.UTC(),
		Summary:    a.Summary,
		URL:        a.URL,
		Labels:     unionLabels(a.Labels, extraLabels, []string{"kind:" + a.Kind}),
		Fields:     fields,
	}, nil
}

// mergeFields returns item.fields (which must be a JSON object) plus the
// envelope keys entity_type, approximate, as_of and stale.
func mergeFields(a wireActivity) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(a.Fields)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("fields is not a JSON object")
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("fields is not a JSON object: %w", err)
	}
	for k, v := range map[string]any{
		"entity_type": a.EntityType,
		"approximate": a.Approximate,
		"as_of":       a.AsOf,
		"stale":       a.Stale,
	} {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = b
	}
	return json.Marshal(m) // map keys are sorted, so the encoding is stable
}

// unionLabels returns the sorted, de-duplicated union of its inputs, so label
// order can never change an entry's content hash.
func unionLabels(sets ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, s := range sets {
		for _, l := range s {
			if _, ok := seen[l]; ok {
				continue
			}
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
