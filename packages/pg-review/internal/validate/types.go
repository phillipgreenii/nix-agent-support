// Package validate is the mechanical gate for a findings batch: it checks one
// findings document against the evidence rows of its run, its query log, the
// lens's closed kind list and the limits, and reports every failure.
package validate

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

// Document is a decoded findings document (the analyst's final message).
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Lens          string    `json:"lens"`
	Scope         string    `json:"scope"`
	Window        Window    `json:"window"`
	Findings      []Finding `json:"findings"`
	Unverified    []string  `json:"unverified"`
	End           bool      `json:"end"`
}

// Window is a time window with RFC 3339 bounds.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Finding is one finding; it is also the stored write-up of a review item.
type Finding struct {
	SubjectKey     string         `json:"subject_key"`
	Kind           string         `json:"kind"`
	Title          string         `json:"title"`
	Category       string         `json:"category"`
	Situation      string         `json:"situation"`
	History        string         `json:"history"`
	Problem        string         `json:"problem"`
	WhyProblem     string         `json:"why_problem"`
	Impact         string         `json:"impact"`
	Options        []Option       `json:"options"`
	Recommendation Recommendation `json:"recommendation"`
	Glossary       []GlossaryTerm `json:"glossary"`
	Evidence       []string       `json:"evidence"`
	Sums           []Sum          `json:"sums"`
	Targets        []string       `json:"targets"`
	ConfirmBy      string         `json:"confirm_by"`
}

// Option is one choice of a finding.
type Option struct {
	Name    string `json:"name"`
	Pros    string `json:"pros"`
	Cons    string `json:"cons"`
	IfRuled string `json:"if_ruled"`
}

// Recommendation is the recommended option and why.
type Recommendation struct {
	Option string `json:"option"`
	Why    string `json:"why"`
}

// GlossaryTerm defines one term of a finding.
type GlossaryTerm struct {
	Term    string `json:"term"`
	Meaning string `json:"meaning"`
}

// Sum is a stated sum of evidence or query values.
type Sum struct {
	Label   string    `json:"label"`
	Addends []float64 `json:"addends"`
	Total   float64   `json:"total"`
}

// EvidenceRow is one evidence row of a run.
type EvidenceRow struct {
	SchemaVersion int                `json:"schema_version"`
	ID            string             `json:"id"`
	SubjectKey    string             `json:"subject_key"`
	DefinedIn     string             `json:"defined_in"`
	Type          string             `json:"type"`
	Kind          string             `json:"kind"`
	Scope         string             `json:"scope"`
	Window        Window             `json:"window"`
	Metrics       map[string]float64 `json:"metrics"`
	Baseline      []BaselineWindow   `json:"baseline"`
	FirstSeen     string             `json:"first_seen"`
	LastSeen      string             `json:"last_seen"`
	Query         string             `json:"query"`
	Samples       []string           `json:"samples"`
}

// BaselineWindow is one baseline window of an evidence row.
type BaselineWindow struct {
	From    string             `json:"from"`
	To      string             `json:"to"`
	Metrics map[string]float64 `json:"metrics"`
}

// QueryEntry is one query log entry, assigned by the broker.
type QueryEntry struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Args          []string `json:"args"`
	Scope         string   `json:"scope"`
	Rows          int      `json:"rows"`
	At            string   `json:"at"`
}

// QueryResult is one persisted query result: a JSON array of rows, indexed
// from 0; a row maps metric keys to values (non-numeric keys are ignored).
type QueryResult []map[string]any

// Input is everything the validator reads about a run.
type Input struct {
	Doc      Document
	Evidence []EvidenceRow
	QueryLog []QueryEntry
	// QueryResults holds the persisted result of each query, keyed by Q id.
	// A Q id with no entry is an unknown id.
	QueryResults map[string]QueryResult
	// Kinds is the lens's closed kind list.
	Kinds []string
	// FieldMaxChars is the per-field character cap (0 means 1200).
	FieldMaxChars int
	// MaxFindings caps the findings of one batch (0 means no cap); the
	// caller passes the lower of the lens max_findings and items_max_per_run.
	MaxFindings int
	// RawText is the staged raw text of the run.
	RawText string
}

// Failure is one rule broken by one finding.
type Failure struct {
	// Index is the finding's index in the batch; -1 for a batch-level rule.
	Index int
	// Finding is the finding's subject_key, or "#<index>" when it has none.
	Finding string
	Field   string
	Rule    string
	Token   string
}

func (f Failure) String() string {
	s := fmt.Sprintf("finding %d (%s): %s: %s", f.Index, f.Finding, f.Field, f.Rule)
	if f.Token != "" {
		s += fmt.Sprintf(" [%s]", f.Token)
	}
	return s
}

// Error is the rejection of a batch: it carries every failure and maps to
// the exit-4 class.
type Error struct{ Failures []Failure }

func (e *Error) Error() string {
	lines := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		lines[i] = f.String()
	}
	return fmt.Sprintf("findings rejected: %d failure(s)\n%s", len(e.Failures), strings.Join(lines, "\n"))
}

// ExitCode is the exit status of a rejected batch.
func (e *Error) ExitCode() int { return exitcode.ValidationRejected }

// Rule names.
const (
	RuleUnknownKind       = "unknown-kind"
	RuleUnknownSubject    = "unknown-subject"
	RuleUnknownID         = "unknown-evidence-id"
	RuleUnknownMetric     = "unknown-metric"
	RuleCountMismatch     = "count-mismatch"
	RuleSumMismatch       = "sum-mismatch"
	RuleSumAddend         = "sum-addend-not-in-evidence"
	RuleFieldTooLong      = "field-too-long"
	RuleMissingSection    = "missing-section"
	RuleMissingConfirmBy  = "missing-confirm-by"
	RuleUndefinedTerm     = "undefined-glossary-term"
	RuleRawTextOverlap    = "raw-text-overlap"
	RuleCopiedURL         = "url-copied-from-evidence"
	RuleCopiedCommand     = "command-copied-from-evidence"
	RuleTooManyFindings   = "too-many-findings"
	RuleOptionsIncomplete = "options-incomplete"
)
