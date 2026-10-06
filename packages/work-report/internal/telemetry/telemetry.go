// Package telemetry implements work-report's telemetry declaration: one JSONL
// line per source per pull in log/pull.jsonl and one line per report in
// log/report.jsonl, under $XDG_STATE_HOME/work-report. No OpenTelemetry or
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
	pullFile   = "pull.jsonl"
	reportFile = "report.jsonl"
	logDirName = "log"
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
}

// ReportLog is one record per rendered report or report outcome. Status is
// "rendered" or "outcome".
type ReportLog struct {
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Since      string    `json:"since"`
	Before     string    `json:"before"`
	Reason     string    `json:"reason"`
	DurationMS int64     `json:"duration_ms"`
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
func (l *Logger) LogPull(p PullLog) error { return l.append(pullFile, p) }

// LogReport appends one line to log/report.jsonl.
func (l *Logger) LogReport(r ReportLog) error { return l.append(reportFile, r) }

// append writes v as one JSON line, never truncating the file. Errors are
// returned to the caller, which decides whether to ignore them.
func (l *Logger) append(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("telemetry: encode %s record: %w", name, err)
	}
	b = append(b, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	dir := filepath.Join(l.dir(), logDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("telemetry: create log dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("telemetry: open %s: %w", name, err)
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
