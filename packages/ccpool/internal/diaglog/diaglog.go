// Package diaglog is an append-only JSONL OPERATOR-DIAGNOSTIC log: one line per
// diagnostic event, schema {"time":"<RFC3339 UTC>","level":"<lowercase>","msg":"<text>"}.
//
// This is DISTINCT from internal/eventlog (events.jsonl), which records the
// ordered DOMAIN sequence of state transitions and input actions
// (ts/name/kind/...). diaglog replaces the old free-form <state-dir>/hook.log
// plain-text diagnostics so the otelcol filelog receiver can parse it: the
// receiver maps attributes.time -> log timestamp (gotime RFC3339) and
// attributes.level -> OTel severity (severity_parser), then ships it to Loki as
// service_name="ccpool". See phillipgreenii-nix-support-apps
// darwin/modules/observability/registration.nix + darwin/services/otelcol/config.yaml.nix.
//
// Canonical location: <state-dir>/diagnostics.jsonl (beside events.jsonl and the
// old hook.log). One JSON object per line.
//
// Writes use O_APPEND|O_CREATE|O_WRONLY and the fd is NOT held open between
// writes: O_APPEND keeps the small single-line writes atomic across the
// concurrent hook/reply processes that share a pool. A nil *Logger is a valid
// no-op so callers can treat it as an optional dependency, mirroring the
// never-fail diagnostics policy of the hook (spec §9/§15).
package diaglog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// entry is one JSONL line. The lowercase json tags time/level/msg are LOAD-BEARING:
// the otelcol json_parser reads attributes.time and the severity_parser reads
// attributes.level by exactly those names.
type entry struct {
	Time  string `json:"time"`  // RFC3339, UTC
	Level string `json:"level"` // lowercase: error | warn | info
	Msg   string `json:"msg"`
}

// Logger appends JSONL entries. The fd is opened per-write (O_APPEND), never
// held; the mutex serializes in-process writes. Methods are nil-safe.
type Logger struct {
	path string
	mu   sync.Mutex
}

// Open ensures the parent dir exists (0o700) and records the path. It does NOT
// open the file — Log opens it per-write so cross-process O_APPEND stays atomic.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir diaglog parent: %w", err)
	}
	return &Logger{path: path}, nil
}

// Log writes one entry as a single JSON line + "\n". ts is passed explicitly
// (no time.Now inside the package) for deterministic tests; it is normalized to
// RFC3339 UTC. Nil-safe no-op.
func (l *Logger) Log(ts time.Time, level, msg string) error {
	if l == nil {
		return nil
	}
	line, err := json.Marshal(entry{Time: ts.UTC().Format(time.RFC3339), Level: level, Msg: msg})
	if err != nil {
		return fmt.Errorf("marshal diag entry: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open diag log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write diag entry: %w", err)
	}
	return nil
}
