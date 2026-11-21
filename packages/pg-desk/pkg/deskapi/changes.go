package deskapi

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ChangesContract is the contract string every change envelope carries.
const ChangesContract = "pg-desk.changes/v1"

// Source status values.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
	StatusFailed   = "failed"
)

// Ack is the acknowledgement handle of one change item as the pg-desk source
// adapter prints it under deferred acknowledgement: the lane (one independent
// ordered feed, the entity type) and the token (the decimal change_log sequence
// the settle step acknowledges). Both are non-empty strings the router never
// interprets. pg-desk itself does not print the handle; the adapter derives it
// from cursor.to and the record sequences.
type Ack struct {
	Lane  string `json:"lane"`
	Token string `json:"token"`
}

// Cursor is the consumer's change_log position around one call: From is the
// cursor before the call, To the highest seq delivered in it (equal to From
// when nothing was delivered).
type Cursor struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Source is one watched query the call consulted. Reason is set only when
// Status is not ok.
type Source struct {
	Query  string `json:"query"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Record is one change record. ID is pg-desk's canonical entity id for the
// type; Title is display only and MUST NOT be used for decisions. Ack is the
// optional per-item acknowledgement handle, absent unless a deferred
// acknowledgement producer sets it.
type Record struct {
	Seq     int64    `json:"seq"`
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Version int64    `json:"version"`
	Kinds   []string `json:"kinds"`
	Origin  string   `json:"origin"`
	At      string   `json:"at"`
	Ack     *Ack     `json:"ack,omitempty"`
}

// Changes is the body of one `pg-desk <type> changes` call. Consumer is the
// --consumer name of the call; Sources has one row per watched query consulted.
type Changes struct {
	Contract string   `json:"contract"`
	Type     string   `json:"type"`
	Consumer string   `json:"consumer"`
	Cursor   Cursor   `json:"cursor"`
	Sources  []Source `json:"sources"`
	Records  []Record `json:"records"`
}

// EncodeChanges encodes a change envelope as the indented JSON pg-desk prints.
func EncodeChanges(c Changes) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode changes: %w", err)
	}
	return b, nil
}

// DecodeChanges strictly decodes a change envelope: an unknown member is an
// error.
func DecodeChanges(data []byte) (Changes, error) {
	var c Changes
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Changes{}, fmt.Errorf("decode changes: %w", err)
	}
	return c, nil
}
