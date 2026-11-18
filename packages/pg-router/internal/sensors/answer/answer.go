// Package answer is the sensors' output side: the Answer record, the printer
// that emits it as one JSONL line validated against the level-answer schema of
// the router, and the answer cache keyed by observed_at.
package answer

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/pg-router/schemas"
)

// Reading is the optional numeric reading behind a level.
type Reading struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Answer is one sensor answer for one resource (one JSONL line). The wire field
// names are snake_case on purpose.
type Answer struct {
	SchemaVersion string    `json:"schemaVersion"`
	Resource      string    `json:"resource"`
	Level         string    `json:"level"`
	Reading       *Reading  `json:"reading,omitempty"`
	ObservedAt    time.Time `json:"-"`
	ValidForS     int       `json:"valid_for_s"`
	Owner         string    `json:"owner"`
}

// MarshalJSON renders observed_at as RFC 3339 UTC with the Z suffix the schema
// requires.
func (a Answer) MarshalJSON() ([]byte, error) {
	type plain Answer
	return json.Marshal(struct {
		plain
		ObservedAt string `json:"observed_at"`
	}{plain(a), a.ObservedAt.UTC().Format(time.RFC3339Nano)})
}

// UnmarshalJSON reads the wire form back (the cache file).
func (a *Answer) UnmarshalJSON(b []byte) error {
	type plain Answer
	aux := struct {
		*plain
		ObservedAt string `json:"observed_at"`
	}{plain: (*plain)(a)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339Nano, aux.ObservedAt)
	if err != nil {
		return fmt.Errorf("observed_at: %w", err)
	}
	a.ObservedAt = t.UTC()
	return nil
}

// Encode renders the answer as one JSON object (no newline) and validates it
// against the level-answer schema; an answer the schema rejects is an error.
func Encode(a Answer) ([]byte, error) {
	if a.SchemaVersion == "" {
		a.SchemaVersion = "1"
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if err := schemas.ValidateLevelAnswer(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Printer writes validated answers, one JSONL line each.
type Printer struct {
	W io.Writer
}

// Print validates and writes one answer. Nothing is written when validation
// fails.
func (p Printer) Print(a Answer) error {
	raw, err := Encode(a)
	if err != nil {
		return fmt.Errorf("answer for %q: %w", a.Resource, err)
	}
	_, err = p.W.Write(append(raw, '\n'))
	return err
}
