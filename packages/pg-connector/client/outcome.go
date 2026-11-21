package client

import (
	"encoding/json"

	"github.com/phillipgreenii/x/exitcode"
)

// SourceStatus is one row's status in a fan-out response's sources[] array.
type SourceStatus string

const (
	// SourceSucceeded: the source answered normally.
	SourceSucceeded SourceStatus = "succeeded"
	// SourceDegraded: the source answered, but with a failure/bad state.
	SourceDegraded SourceStatus = "degraded"
	// SourceDisabled: the source does not implement/support this op at all
	// (a well-formed "not applicable," not a failure).
	SourceDisabled SourceStatus = "disabled"
)

// SchemaVersion1 is the version an outcome without a schemaVersion field
// reads as. Producers leave the field unset until a later version exists, so
// today's output stays byte-identical.
const SchemaVersion1 = 1

// SourceResult is one row in a fan-out response's sources[] array: one row
// per source queried, never collapsed. Count is that source's own raw
// pre-merge count; for a fan-out op with no natural item count of its own
// (auth_status/capabilities), Count MAY be 0.
type SourceResult struct {
	Source string       `json:"source"`
	Status SourceStatus `json:"status"`
	Count  int          `json:"count"`
	Reason string       `json:"reason,omitempty"`
}

// FanOutOutcome is the response envelope for a fan-out op: one that queries
// every registered backend of a type/capability at once (e.g. auth status
// or config validate).
//
// SchemaVersion is additive and optional: a producer that predates it omits
// the key, and an absent (zero) value reads as version 1 (Version).
type FanOutOutcome struct {
	SchemaVersion int            `json:"schemaVersion,omitempty"`
	Sources       []SourceResult `json:"sources"`
}

// Version returns the outcome's schema version, reading an absent value as 1.
func (o FanOutOutcome) Version() int { return versionOrOne(o.SchemaVersion) }

func versionOrOne(v int) int {
	if v == 0 {
		return SchemaVersion1
	}
	return v
}

// ExitCode returns this outcome's pg-connector CLI exit code: 0 (all
// healthy — succeeded or disabled), 5 (Degraded — at least one source is
// degraded, but at least one is healthy), or 3 (Unavailable, total failure —
// no healthy source, including the case of zero sources queried). 1 is
// never returned here — it is reserved for the CLI's own
// generic/unexpected-failure path outside this helper.
//
// Disabled counts as healthy, not as a failure: a no-credential backend
// that correctly answers "not applicable" is a well-formed negative, not a
// degraded state.
func (o FanOutOutcome) ExitCode() int {
	healthy, failed := 0, 0
	for _, s := range o.Sources {
		if s.Status == SourceDegraded {
			failed++
		} else {
			healthy++
		}
	}
	switch {
	case failed == 0 && healthy > 0:
		return exitcode.OK
	case healthy == 0:
		return exitcode.Unavailable
	default:
		return exitcode.Degraded
	}
}

// ListOutcome is a "list" verb's response for entity type E: every queried
// backend's matched entities concatenated into Entities, with each backend's
// own health as one row in Sources (never collapsed).
type ListOutcome[E any] struct {
	Entities   []E            `json:"entities"`
	PresentIDs []string       `json:"present_ids"`
	Sources    []SourceResult `json:"sources"`
	// Truncated is true when ANY queried backend's list result was
	// truncated, so a caller comparing this listing against a prior one can
	// withhold removals it can no longer trust.
	Truncated bool `json:"truncated"`
	// Fingerprints maps each entity id in Entities to its fingerprint string;
	// present only when the call passed --fingerprints.
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

// ExitCode delegates to the shared fan-out scheme (0/5/3).
func (o ListOutcome[E]) ExitCode() int { return FanOutOutcome{Sources: o.Sources}.ExitCode() }

// CIListOutcome is "ci list"'s response for run type R: every registered ci
// backend's runs concatenated into Runs, with each backend's own health as
// one row in Sources.
type CIListOutcome[R any] struct {
	Runs    []R            `json:"runs"`
	Sources []SourceResult `json:"sources"`
}

// ExitCode delegates to the shared fan-out scheme (0/5/3).
func (o CIListOutcome[R]) ExitCode() int { return FanOutOutcome{Sources: o.Sources}.ExitCode() }

// ChangeKind mirrors the wire "change" field values.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeChanged ChangeKind = "changed"
	ChangeRemoved ChangeKind = "removed"
)

// Change is one item in a changes response's changes[] array. Seq, Kinds and
// Fields are set ONLY on an entry a forwarded source produced; omitted
// otherwise. Seq is a pointer because 0 (a --reset replay entry) is a value
// the forwarded wire carries.
type Change struct {
	Change ChangeKind      `json:"change"`
	Source string          `json:"source"`
	Entity json.RawMessage `json:"entity"`
	Seq    *int64          `json:"seq,omitempty"`
	Kinds  json.RawMessage `json:"kinds,omitempty"`
	Fields json.RawMessage `json:"fields,omitempty"`
}

// ChangesSource is one item in a changes response's sources[] array.
type ChangesSource struct {
	Backend   string       `json:"backend"`
	Status    SourceStatus `json:"status"`
	Version   int64        `json:"version"`
	Truncated bool         `json:"truncated"`
	Reason    string       `json:"reason,omitempty"`
	// NextSeq is set only on a forwarded source: the backend's log position
	// scanned up to, which the caller acknowledges (changes-ack) after the
	// output is processed.
	NextSeq *int64 `json:"next_seq,omitempty"`
	// StoreIdentity is set only on a forwarded source: the identity of the
	// backend's own store, passed through unchanged.
	StoreIdentity string `json:"store_identity,omitempty"`
}

// ChangesOutcome is a "changes" verb's response.
type ChangesOutcome struct {
	Sources []ChangesSource `json:"sources"`
	Changes []Change        `json:"changes"`
	// SourcesFreshness is a JSON array, set only when at least one source was
	// forwarded; kept raw so this package needs no engine type, and so an
	// empty list still writes [].
	SourcesFreshness json.RawMessage `json:"sources_freshness,omitempty"`
}

// ExitCode classifies the sources with the shared fan-out scheme (0/5/3).
func (o ChangesOutcome) ExitCode() int {
	rows := make([]SourceResult, 0, len(o.Sources))
	for _, s := range o.Sources {
		rows = append(rows, SourceResult{Source: s.Backend, Status: s.Status})
	}
	return FanOutOutcome{Sources: rows}.ExitCode()
}
