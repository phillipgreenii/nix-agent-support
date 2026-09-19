package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Verdict is what one delegate call contributed, used both for the merge
// computation (dispatch.go) and the attribution log below.
type Verdict string

const (
	// VerdictApplied is a valid, non-empty response the merge folded in.
	VerdictApplied Verdict = "applied"
	// VerdictAbstain is a valid {} (NoOpinion) response.
	VerdictAbstain Verdict = "abstain"
	// VerdictError covers a non-zero exit, a command that doesn't exist or
	// isn't executable, stdout that failed to parse as hookSpecificOutput,
	// or a context-cancelled kill (hang past budget) — all degrade
	// identically to Abstain-for-this-call (ADR 0071 §2.4/§4 Phase B, B1
	// "Adversarial delegate behavior").
	VerdictError Verdict = "error"
	// VerdictSkipped is a delegate never invoked at all because the total
	// chain budget was already exhausted before its turn.
	VerdictSkipped Verdict = "skipped"
)

// maxAttributionLogBytes is the size threshold at which the attribution log
// rotates: the current file is renamed to a ".1" suffix (replacing any
// prior ".1") and a fresh file is started. Single-generation rotation is
// deliberately simple — this log is diagnostic/attribution data, not an
// audit trail requiring unbounded retention.
const maxAttributionLogBytes = 10 * 1024 * 1024

// AttributionEntry is one JSON-lines record, one per delegate invocation,
// per ADR 0071 §4 Phase B, B1 "Attribution/observability, with a concrete
// format".
type AttributionEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	HookEventName string    `json:"hook_event_name"`
	DelegateName  string    `json:"delegate_name"`
	Contract      string    `json:"contract"`
	Verdict       Verdict   `json:"verdict"`
	DurationMS    int64     `json:"duration_ms"`
}

// AttributionLogger appends one JSON-lines entry per delegate invocation to
// a rotating log file under the router's own CLAUDE_PLUGIN_DATA. It never
// writes to stdout, which is reserved for the router's final merged
// hookSpecificOutput.
type AttributionLogger struct {
	path string
}

// NewAttributionLogger returns a logger writing to
// <dataDir>/attribution.jsonl, creating dataDir if it does not already
// exist.
func NewAttributionLogger(dataDir string) (*AttributionLogger, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create attribution log dir %q: %w", dataDir, err)
	}
	return &AttributionLogger{path: filepath.Join(dataDir, "attribution.jsonl")}, nil
}

// Log appends entry, rotating the log first if it has grown past
// maxAttributionLogBytes. A nil *AttributionLogger is a safe no-op, so
// callers that couldn't construct one (e.g. CLAUDE_PLUGIN_DATA unset) can
// still call Log unconditionally.
func (l *AttributionLogger) Log(entry AttributionEntry) error {
	if l == nil {
		return nil
	}
	l.rotateIfNeeded()

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal attribution entry: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open attribution log %q: %w", l.path, err)
	}
	defer f.Close()
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write attribution log %q: %w", l.path, err)
	}
	return nil
}

func (l *AttributionLogger) rotateIfNeeded() {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < maxAttributionLogBytes {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
}
