package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// issueVolatileKeys are the object keys NormalizeIssuePayload drops at every
// depth of an issue payload: they move without the issue changing (INV-ICD-6,
// INV-ICD-7). as_of, stale, updated and updated_at are the read-time and
// last-touched stamps, served_from and age_seconds are the umbrella's
// read-through-cache provenance, groups is the read answer's per-group
// freshness bookkeeping (fetched_at and fresh_until move on every --fresh
// read of an unchanged issue), and entity_meta is connector metadata that can
// flip without a content change.
var issueVolatileKeys = map[string]struct{}{
	"as_of":       {},
	"stale":       {},
	"updated":     {},
	"updated_at":  {},
	"served_from": {},
	"age_seconds": {},
	"groups":      {},
	"entity_meta": {},
}

// issueFingerprintExcludes are the dotted entity-field paths the connector's
// issue backends declare volatile for the umbrella's list fingerprint
// (IssueListResult.FingerprintExcludes; the beads backend declares
// metadata.last_checked_at). A path is resolved from every object of the
// payload, so the entry applies to issue_show and to each nested issue alike.
var issueFingerprintExcludes = []string{"metadata.last_checked_at"}

// NormalizeIssuePayload returns the canonical JSON of an issue payload
// (gather.IssueFacts, or any JSON document) with the volatile keys of
// issueVolatileKeys and the connector's fingerprint-exclude fields removed at
// ANY depth: the document is decoded, stripped, and re-encoded with sorted
// object keys and no insignificant whitespace, so two payloads that differ
// only in volatile fields yield identical bytes while a real field difference
// yields different bytes. Numbers are carried verbatim. The input is not
// modified; an undecodable input is an error.
func NormalizeIssuePayload(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("pipeline: normalise issue payload: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("pipeline: normalise issue payload: trailing data after the JSON document")
	}
	stripIssueVolatile(v)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // map keys encode sorted
		return nil, fmt.Errorf("pipeline: normalise issue payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// stripIssueVolatile removes, in place and at every depth, the volatile keys
// and the fingerprint-exclude paths.
func stripIssueVolatile(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range issueVolatileKeys {
			delete(t, k)
		}
		for _, path := range issueFingerprintExcludes {
			deleteDotted(t, strings.Split(path, "."))
		}
		for _, c := range t {
			stripIssueVolatile(c)
		}
	case []any:
		for _, c := range t {
			stripIssueVolatile(c)
		}
	}
}

// deleteDotted deletes the value at path (one nesting level per element)
// below m; a missing or non-object step is a no-op.
func deleteDotted(m map[string]any, path []string) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(m, path[0])
		return
	}
	if next, ok := m[path[0]].(map[string]any); ok {
		deleteDotted(next, path[1:])
	}
}

// issueCompareBytes is the payload an issue entity is compared and hashed
// by: the normalised bytes, or the raw bytes when they cannot be normalised
// (an unreadable payload must never be read as "nothing changed").
func issueCompareBytes(raw []byte) []byte {
	if n, err := NormalizeIssuePayload(raw); err == nil {
		return n
	}
	return raw
}

// issueUpdatedAt is the raw issue_show.updated_at of an issue payload, "" when
// absent or undecodable. It is read BEFORE normalisation (which drops it) for
// classify.Snapshot.Updated.
func issueUpdatedAt(raw []byte) string {
	var f struct {
		IssueShow struct {
			UpdatedAt string `json:"updated_at"`
		} `json:"issue_show"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return ""
	}
	return f.IssueShow.UpdatedAt
}
