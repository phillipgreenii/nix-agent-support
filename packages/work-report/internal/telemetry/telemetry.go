// Package telemetry implements work-report's telemetry declaration: one JSONL
// line per source per pull in log/pull.jsonl, one line per failed
// degraded-source tracker call in log/reconcile.jsonl, and one line per report
// in log/report.jsonl, under $XDG_STATE_HOME/work-report. No OpenTelemetry or
// Prometheus metrics are emitted in v1.
package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	pullFile      = "pull.jsonl"
	reportFile    = "report.jsonl"
	reconcileFile = "reconcile.jsonl"
	logDirName    = "log"
)

// PullLog is one per-source record of a pull. Since and Before are RFC3339,
// or "" when the range is open-ended.
type PullLog struct {
	TS         time.Time `json:"ts"`
	Source     string    `json:"source"`
	Since      string    `json:"since"`
	Before     string    `json:"before"`
	Status     string    `json:"status"`
	Count      int       `json:"count"`
	Unchanged  int       `json:"unchanged"`
	Rejected   int       `json:"rejected"`
	Truncated  bool      `json:"truncated"`
	DurationMS int64     `json:"duration_ms"`
	Reason     string    `json:"reason"`
	// Retried is true when the source degraded on a deadline in the first
	// attempt and the pull re-invoked it once (the status and reason are those
	// of the retry).
	Retried bool `json:"retried,omitempty"`
}

// ReconcileLog is one failed tracker call of the degraded-source reconcile
// (a bead that could not be created, appended to or closed, or the lookup
// itself). It exists so a refused close is visible somewhere an operator or a
// probe reads, not only on stderr. Source and Bead are "" when not applicable.
type ReconcileLog struct {
	TS     time.Time `json:"ts"`
	Source string    `json:"source"`
	Action string    `json:"action"`
	Bead   string    `json:"bead"`
	Error  string    `json:"error"`
}

// ReportRange is the range of a report run as report.jsonl records it. Since is
// null for an open-ended range and Before is empty when the range could not be
// resolved.
type ReportRange struct {
	Spec   string  `json:"spec"`
	Since  *string `json:"since"`
	Before string  `json:"before"`
}

// ReportNarrowing is the applied narrowing of a report run.
type ReportNarrowing struct {
	Labels  []string `json:"labels"`
	Sources []string `json:"sources"`
	Types   []string `json:"types"`
}

// StderrTailKeep is how much of a failed run's generator stderr its log line
// carries.
const StderrTailKeep = 2 << 10

// ReportLog is one record per report run. Status is ok or failed, the same
// values as manifest.json, and OutcomeKind the closed set of the run record.
// Preset, GeneratorExit and OutputDir are null when they do not exist (an ad
// hoc run, a generator that never ran or was killed by a signal, no run
// directory). StderrTail is present only on a failed run: the last
// StderrTailKeep bytes of the generator's stderr, which is untrusted and may
// echo entry text, so the file is mode 0600.
type ReportLog struct {
	TS            time.Time       `json:"ts"`
	RunID         string          `json:"run_id"`
	Preset        *string         `json:"preset"`
	Generator     string          `json:"generator"`
	Status        string          `json:"status"`
	OutcomeKind   string          `json:"outcome_kind"`
	GeneratorExit *int            `json:"generator_exit"`
	Reason        string          `json:"reason"`
	Range         ReportRange     `json:"range"`
	Narrowing     ReportNarrowing `json:"narrowing"`
	Overrides     []string        `json:"overrides"`
	EntryCount    int             `json:"entry_count"`
	FileCount     int             `json:"file_count"`
	OutputDir     *string         `json:"output_dir"`
	ToolVersion   string          `json:"tool_version"`
	GeneratorMS   int64           `json:"generator_ms"`
	TotalMS       int64           `json:"total_ms"`
	StderrTail    *string         `json:"stderr_tail,omitempty"`
}

// Logger appends JSONL records under its state directory. It is safe for
// concurrent use.
type Logger struct {
	stateDir string
	mu       sync.Mutex
}

// New returns a Logger rooted at stateDir. An empty stateDir selects
// $XDG_STATE_HOME/work-report, falling back to $HOME/.local/state/work-report.
// Nothing is created until the first write.
func New(stateDir string) *Logger {
	return &Logger{stateDir: stateDir}
}

func (l *Logger) dir() string {
	if l.stateDir != "" {
		return l.stateDir
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "work-report")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "work-report")
}

// LogPull appends one line to log/pull.jsonl.
func (l *Logger) LogPull(p PullLog) error { return l.append(pullFile, p, 0o644) }

// LogReconcile appends one line to log/reconcile.jsonl.
func (l *Logger) LogReconcile(r ReconcileLog) error { return l.append(reconcileFile, r, 0o644) }

// LogReport appends one line to log/report.jsonl, created mode 0600 (and
// chmodded to 0600 when it already exists with another mode) because a failed
// run's stderr tail is untrusted text. Nil lists are written as [].
func (l *Logger) LogReport(r ReportLog) error {
	r.Overrides = nonNil(r.Overrides)
	r.Narrowing.Labels = nonNil(r.Narrowing.Labels)
	r.Narrowing.Sources = nonNil(r.Narrowing.Sources)
	r.Narrowing.Types = nonNil(r.Narrowing.Types)
	return l.append(reportFile, r, 0o600)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// append writes v as one JSON line, never truncating the file. Errors are
// returned to the caller, which decides whether to ignore them.
func (l *Logger) append(name string, v any, mode os.FileMode) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("telemetry: encode %s record: %w", name, err)
	}
	b = append(b, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	dir := filepath.Join(l.dir(), logDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("telemetry: create log dir: %w", err)
	}
	// The directory is shared by every log file and may hold untrusted text, so
	// its mode is 0700 even when it already existed with a wider one.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("telemetry: secure log dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("telemetry: open %s: %w", name, err)
	}
	// The umask must not widen or narrow the documented mode, and a file that
	// already existed keeps its old one unless it is chmodded.
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("telemetry: chmod %s: %w", name, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("telemetry: write %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("telemetry: close %s: %w", name, err)
	}
	return nil
}
