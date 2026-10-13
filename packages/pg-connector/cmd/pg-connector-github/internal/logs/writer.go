package logs

import (
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
)

// ServiceName is the Loki service_name of the event log and the structured log.
const ServiceName = "pg-connector-github"

// ClientServiceName is the Loki service_name of the client log.
const ClientServiceName = "pg-connector-github-client"

// DaemonMaxBytes is the size past which the daemon rotates the event log and
// the structured log (5 MiB, the shared writer's MaxBytes).
const DaemonMaxBytes int64 = evlog.MaxBytes

// ClientMaxBytes is the size past which the client rotates the client log
// (1 MiB): the client writes it exactly when the daemon is down, so it stays
// small.
const ClientMaxBytes int64 = 1 << 20

// Row kinds of the event log and the client log.
const (
	KindRequest = "request"
	KindOrigin  = "origin"
)

// Outcomes of a row. A request or origin row carries OutcomeOK on success and
// OutcomeError on a failure whose wire code is in error_code; the client log's
// row carries OutcomeDaemonUnreachable.
const (
	OutcomeOK                = "ok"
	OutcomeError             = "error"
	OutcomeDaemonUnreachable = "daemon_unreachable"
)

// timeLayout is the JSONL standard's timestamp layout, the one pkg/eventlog's
// Base uses.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// EventLogPath is the event log's path inside stateDir.
func EventLogPath(stateDir string) string { return filepath.Join(stateDir, config.EventLogFileName) }

// DaemonLogPath is the structured log's path inside stateDir.
func DaemonLogPath(stateDir string) string { return filepath.Join(stateDir, config.DaemonLogFileName) }

// ClientLogPath is the client log's path inside stateDir.
func ClientLogPath(stateDir string) string { return filepath.Join(stateDir, config.ClientLogFileName) }

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

// header is the field set every row of the origin and structured logs starts
// with (the request and client rows start with evlog.Base instead, which adds
// op and duration_ms).
type header struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Service string `json:"service"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid"`
}

func newHeader(t time.Time, level, msg, version string) header {
	return header{Time: stamp(t), Level: level, Msg: msg, Service: ServiceName, Version: version, PID: os.Getpid()}
}

// write appends v as one line to path, rotating past max. Errors are swallowed.
func write(path string, max int64, v any) { evlog.WriteBestEffort(path, max, v) }
