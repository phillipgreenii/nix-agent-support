package parity

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// Side says which side planned an entry the other did not.
type Side string

const (
	SideOld Side = "old-only"
	SideNew Side = "new-only"
)

// Difference is one entry present on one side only.
type Difference struct {
	Entry
	Side Side
}

// Expected is one listed, documented difference. ID is the design decision the
// reason rests on; UNOBS is not a design exception but a statement about the
// harness: the old side exposes no write against an existing bead, so the new
// side's entry has no observable counterpart (spec 7.3 states the rule's
// behaviour; the entry is pinned so it cannot drift unseen).
type Expected struct {
	ID     string `json:"id"`
	Entity string `json:"entity"`
	Kind   string `json:"kind"`
	Op     string `json:"op"`
	Side   Side   `json:"side"`
	Reason string `json:"reason"`
}

func (x Expected) difference() Difference {
	return Difference{Entry: Entry{x.Entity, x.Kind, x.Op}, Side: x.Side}
}

// Report is the diff of one scenario.
type Report struct {
	Scenario    string
	Matched     []Entry      // planned identically by both sides
	Expected    []Expected   // listed differences that occurred
	Unexplained []Difference // differences nobody listed: FAIL
	Missing     []Expected   // listed differences that did not occur: FAIL
}

// Clean is true when nothing is unexplained and no listed exception is missing.
func (r Report) Clean() bool { return len(r.Unexplained) == 0 && len(r.Missing) == 0 }

// Diff compares the two normalized sides of a scenario against its expected
// differences. Entries are counted, not just present: two identical entries are
// two writes, and a listing covers one occurrence.
func Diff(scenario string, old, new []Entry, expected []Expected) Report {
	r := Report{Scenario: scenario}
	oldN, newN := map[Entry]int{}, map[Entry]int{}
	var keys []Entry
	for _, e := range old {
		if oldN[e] == 0 && newN[e] == 0 {
			keys = append(keys, e)
		}
		oldN[e]++
	}
	for _, e := range new {
		if oldN[e] == 0 && newN[e] == 0 {
			keys = append(keys, e)
		}
		newN[e]++
	}
	sortEntries(keys)
	var diffs []Difference
	for _, k := range keys {
		m := min(oldN[k], newN[k])
		for i := 0; i < m; i++ {
			r.Matched = append(r.Matched, k)
		}
		for i := m; i < oldN[k]; i++ {
			diffs = append(diffs, Difference{k, SideOld})
		}
		for i := m; i < newN[k]; i++ {
			diffs = append(diffs, Difference{k, SideNew})
		}
	}
	used := make([]bool, len(diffs))
	for _, x := range expected {
		found := false
		for i, d := range diffs {
			if !used[i] && d == x.difference() {
				used[i] = true
				found = true
				break
			}
		}
		if found {
			r.Expected = append(r.Expected, x)
		} else {
			r.Missing = append(r.Missing, x)
		}
	}
	for i, d := range diffs {
		if !used[i] {
			r.Unexplained = append(r.Unexplained, d)
		}
	}
	return r
}

//go:embed testdata/expected-diff.json
var expectedDiffJSON []byte

// knownIDs are the exception ids the expected diff may use: the design's
// decision ids (S13..S26), UNOBS, and "pg2-nbkps", the operator ruling (Phillip,
// 2026-10-05) that scenario 13's title-prefix adoption is out of scope. That id
// names a ruling, not a design-spec section. "D-F13" is the daily-focus
// decision that one focus bead per source entity is minted, held and released
// by the decider (the focus.item rule), which has no counterpart on the old side.
var knownIDs = map[string]bool{
	"S13": true, "S14": true, "S15": true, "S16": true, "S19": true, "S24": true, "S26": true,
	"UNOBS": true, "pg2-nbkps": true, "D-F13": true,
}

// LoadExpected reads the embedded testdata/expected-diff.json, keyed by
// scenario name.
func LoadExpected() (map[string][]Expected, error) { return ParseExpected(expectedDiffJSON) }

// ParseExpected decodes and validates an expected-diff document. Unknown
// members, unknown ids or sides and empty entity, kind, op or reason are
// errors, so the list cannot hold a vague or misspelled allowance.
func ParseExpected(data []byte) (map[string][]Expected, error) {
	var doc struct {
		Scenarios map[string][]Expected `json:"scenarios"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parity: decode expected-diff: %w", err)
	}
	for name, list := range doc.Scenarios {
		for i, x := range list {
			where := fmt.Sprintf("parity: expected-diff %s[%d]", name, i)
			switch {
			case !knownIDs[x.ID]:
				return nil, fmt.Errorf("%s: unknown exception id %q", where, x.ID)
			case x.Side != SideOld && x.Side != SideNew:
				return nil, fmt.Errorf("%s: side %q is neither %s nor %s", where, x.Side, SideOld, SideNew)
			case x.Entity == "" || x.Kind == "" || x.Op == "":
				return nil, errors.New(where + ": entity, kind and op are all required")
			case x.Reason == "":
				return nil, errors.New(where + ": a reason is required")
			}
		}
	}
	if doc.Scenarios == nil {
		doc.Scenarios = map[string][]Expected{}
	}
	return doc.Scenarios, nil
}
