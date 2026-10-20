// Package runner executes a registered report generator under contract
// version 1 and keeps the per-run record.
//
// A run creates a unique per-run directory inside an output parent, writes the
// envelope (the matching entries, the range, the narrowing and the preset
// config) to the generator's stdin, runs the generator as an argv vector with
// an allowlisted environment, a timeout and process-group control, validates
// the manifest it prints on stdout, and writes stderr.log and manifest.json
// (last, atomically) into the run directory. See docs/adr for the contract's
// JSON shapes, exit codes, paths and file names.
//
// The runner never filters: the caller (the report command) applies the range
// and narrowing and passes exactly the matching entries, in baseline order
// (INV-GEN-PLUGIN-1, decision D2).
package runner

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// ContractVersion is the contract version this work-report speaks.
const ContractVersion = 1

// SupportedContractVersions lists every contract version a generator's
// manifest may declare.
var SupportedContractVersions = []int{ContractVersion}

// Modes of an invocation (the envelope's mode field is authoritative; the
// WORK_REPORT_MODE environment variable mirrors it).
const (
	ModeGenerate = "generate"
	ModeValidate = "validate"
)

// File names work-report owns inside a run directory.
const (
	ManifestName = "manifest.json"
	StderrName   = "stderr.log"
	LatestLink   = "latest"
)

// Outcome kinds: the closed set recorded in manifest.json and the telemetry
// log.
const (
	KindOK                  = "ok"
	KindTimeout             = "timeout"
	KindCancelled           = "cancelled"
	KindExitNonzero         = "exit_nonzero"
	KindConfigInvalid       = "config_invalid"
	KindInvalidManifest     = "invalid_manifest"
	KindNotFound            = "not_found"
	KindContractVersion     = "contract_version"
	KindStdoutCap           = "stdout_cap"
	KindOutputDirUnwritable = "output_dir_unwritable"
)

// Status values of a run record.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// Limits of the contract.
const (
	// StdoutCap is the most stdout accepted (exactly this many bytes is fine).
	StdoutCap = 1 << 20
	// StderrKeep is how much of the start and of the end of stderr is kept.
	StderrKeep = 32 << 10
	// MaxFiles, MaxFileSize and MaxTotalSize bound a manifest's files.
	MaxFiles     = 1000
	MaxFileSize  = 1 << 30
	MaxTotalSize = 4 << 30
	// MaxCollisionRetries bounds the run-directory name retries.
	MaxCollisionRetries = 10
)

// Narrowing is the applied narrowing, informational for the generator.
type Narrowing struct {
	Labels  []string `json:"labels"`
	Sources []string `json:"sources"`
	Types   []string `json:"types"`
}

// Range is the resolved range with the spec string and zone name the
// envelope and the run record also carry.
type Range struct {
	rangespec.Range
	Spec string
	Zone string
}

// FooterItem is one source's footer line.
type FooterItem struct {
	SourceID string
	Count    int
	// LastSuccessAt is the zero time for a source that has never pulled.
	LastSuccessAt time.Time
	// LastOutcome is the store's per-source last pull status ("" when never
	// pulled).
	LastOutcome string
}

// BuiltinFunc is an in-process generator (the built-in baseline). It writes
// its files into dir and returns the manifest it would print. It goes through
// the same directory, validation, hashing and manifest.json pipeline as an
// external generator.
type BuiltinFunc func(ctx context.Context, env Envelope, dir string) (GeneratorManifest, error)

// Request is one run.
type Request struct {
	// Generator is the registered generator. A Builtin generator needs the
	// Builtin hook below instead of a command.
	Generator config.Generator
	// Builtin runs the generator in-process when set; generator.argv0 is
	// recorded as builtin:<name>.
	Builtin BuiltinFunc
	// Preset is the preset name; empty records null (an ad hoc run).
	Preset string
	// Overrides names the command-line flags that overrode the preset (flag
	// names only).
	Overrides []string
	// ExtraWarnings are added to the run record's warnings (for example one
	// per degraded source).
	ExtraWarnings []string
	Range         Range
	Narrowing     Narrowing
	// Entries are exactly the matching entries, in baseline order.
	Entries []store.Entry
	Footer  []FooterItem
	// Baseline is the baseline rendering of Entries; it reaches the envelope
	// exactly when the generator's Inputs contains "baseline".
	Baseline string
	// Config is the preset's config object (nil is sent as {}).
	Config map[string]any
	// OutputParent is the absolute directory the per-run child is created in.
	OutputParent string
}

// Outcome is the result of Run.
type Outcome struct {
	// Record is the whole run record: what manifest.json holds.
	Record Record
	// RunDir is the per-run directory ("" when the run failed before one
	// existed).
	RunDir string
	// Stderr is the retained generator stderr (first and last StderrKeep
	// bytes).
	Stderr string
	// Notices are runner-level messages for the operator that are not part of
	// the record (for example a non-symlink latest left alone).
	Notices []string
}

// OK reports whether the run succeeded.
func (o Outcome) OK() bool { return o.Record.Status == StatusOK }

// Envelope is the JSON object written to the generator's stdin.
type Envelope struct {
	ContractVersion int              `json:"contract_version"`
	Mode            string           `json:"mode"`
	RunID           string           `json:"run_id"`
	Report          EnvelopeReport   `json:"report"`
	Range           EnvelopeRange    `json:"range"`
	Narrowing       Narrowing        `json:"narrowing"`
	Entries         []EnvelopeEntry  `json:"entries"`
	Footer          []EnvelopeFooter `json:"footer"`
	Baseline        *string          `json:"baseline,omitempty"`
	Config          map[string]any   `json:"config"`
	OutputDir       string           `json:"output_dir"`
}

// EnvelopeReport names the preset (null for an ad hoc run) and the generator.
type EnvelopeReport struct {
	Preset    *string `json:"preset"`
	Generator string  `json:"generator"`
}

// EnvelopeRange is the resolved range; since is null when open_start.
type EnvelopeRange struct {
	Spec      string  `json:"spec"`
	Since     *string `json:"since"`
	Before    string  `json:"before"`
	OpenStart bool    `json:"open_start"`
	Zone      string  `json:"zone"`
}

// EnvelopeEntry is one entry.
type EnvelopeEntry struct {
	ID         string          `json:"id"`
	ExternalID string          `json:"external_id"`
	SourceID   string          `json:"source_id"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurred_at"`
	Summary    string          `json:"summary"`
	URL        string          `json:"url"`
	Labels     []string        `json:"labels"`
	Fields     json.RawMessage `json:"fields"`
}

// EnvelopeFooter is one source's footer item.
type EnvelopeFooter struct {
	SourceID      string  `json:"source_id"`
	Count         int     `json:"count"`
	LastSuccessAt *string `json:"last_success_at"`
	LastOutcome   string  `json:"last_outcome"`
}

// GeneratorManifest is the manifest a generator prints on stdout.
type GeneratorManifest struct {
	ContractVersion int            `json:"contract_version"`
	Files           []ManifestFile `json:"files"`
	Summary         string         `json:"summary,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
}

// ManifestFile is one file a generator lists.
type ManifestFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Title       string `json:"title,omitempty"`
}

// Record is the run record: the content of manifest.json.
type Record struct {
	Status          string  `json:"status"`
	OutcomeKind     string  `json:"outcome_kind"`
	Reason          string  `json:"reason,omitempty"`
	GeneratorExit   *int    `json:"generator_exit"`
	Signal          *string `json:"signal"`
	TimedOut        bool    `json:"timed_out"`
	ToolVersion     string  `json:"tool_version"`
	ContractVersion int     `json:"contract_version"`
	RunID           string  `json:"run_id"`
	StartedAt       string  `json:"started_at"`
	EndedAt         string  `json:"ended_at"`
	GeneratorMS     int64   `json:"generator_ms"`
	TotalMS         int64   `json:"total_ms"`
	// Preset is null for a run with --generator.
	Preset       *string         `json:"preset"`
	Generator    RecordGenerator `json:"generator"`
	Config       map[string]any  `json:"config"`
	ConfigSHA256 string          `json:"config_sha256"`
	InputSHA256  string          `json:"input_sha256"`
	Range        EnvelopeRange   `json:"range"`
	Narrowing    Narrowing       `json:"narrowing"`
	EntryCount   int             `json:"entry_count"`
	Overrides    []string        `json:"overrides"`
	// Files, Summary and Warnings are present for an ok run only.
	Files    []RecordFile `json:"files,omitempty"`
	Summary  string       `json:"summary,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
}

// RecordGenerator names the generator and what was run.
type RecordGenerator struct {
	Name  string `json:"name"`
	Argv0 string `json:"argv0"`
}

// RecordFile is one validated file of an ok run.
type RecordFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Title       string `json:"title,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}
