// Package wire holds what the daemon and the command share: the protocol
// version, the error names with their exit codes, and the stable answer shapes.
// The framing is HTTP/1.1 with JSON bodies over a Unix socket: GET /v1/hello
// and POST /v1/<operation>.
package wire

import "net/http"

// ProtocolVersion is the wire protocol version reported by status and hello.
const ProtocolVersion = 1

// ClientHeader carries the caller's --client name.
const ClientHeader = "X-Recorder-Client"

// Exit codes of the recorder command (independent of the control command's).
const (
	ExitOK         = 0
	ExitUnexpected = 1
	ExitUsage      = 2
	ExitOffline    = 3
	ExitNotFound   = 4
	ExitDegraded   = 5
	ExitTimeout    = 6
	ExitCursor     = 7
	ExitRefused    = 8
	ExitMismatch   = 9
)

// Stable error names. The wire names are the daemon's; daemon_offline is the
// client's own.
const (
	ErrBadRequest    = "bad_request"
	ErrUnavailable   = "unavailable"
	ErrNotFound      = "not_found"
	ErrBusy          = "busy"
	ErrBehind        = "behind"
	ErrCursorExpired = "cursor_expired"
	ErrInternal      = "internal"
	ErrDaemonOffline = "daemon_offline"
	ErrUsage         = "usage"
	ErrWaitTimeout   = "wait_timeout"
	// Exit 8 preconditions.
	ErrRemovalDisabled = "removal_disabled"
	ErrTargetExists    = "target_exists"
	ErrHeldByLive      = "held_by_live_process"
)

// ExitFor maps a stable error name to the recorder command's exit code.
func ExitFor(name string) int {
	switch name {
	case ErrBadRequest, ErrUsage:
		return ExitUsage
	case ErrWaitTimeout:
		return ExitTimeout
	case ErrUnavailable, ErrDaemonOffline:
		return ExitOffline
	case ErrNotFound:
		return ExitNotFound
	case ErrBusy, ErrBehind:
		return ExitDegraded
	case ErrCursorExpired:
		return ExitCursor
	case ErrRemovalDisabled, ErrTargetExists, ErrHeldByLive:
		return ExitRefused
	}
	return ExitUnexpected
}

// HTTPStatus maps an error name to the response status.
func HTTPStatus(name string) int {
	switch name {
	case ErrBadRequest:
		return http.StatusBadRequest
	case ErrNotFound:
		return http.StatusNotFound
	case ErrUnavailable:
		return http.StatusServiceUnavailable
	case ErrBusy, ErrBehind:
		return http.StatusTooManyRequests
	case ErrCursorExpired:
		return http.StatusGone
	case ErrRemovalDisabled, ErrTargetExists, ErrHeldByLive:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// Error is the JSON body of an error answer.
type Error struct {
	Name    string `json:"error"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Name + ": " + e.Message }

// Errf builds an Error.
func Errf(name, msg string) *Error { return &Error{Name: name, Message: msg} }

// Status is the stable status --json shape, plus later additions (the keys
// after protocol_version).
type Status struct {
	Healthy        bool           `json:"healthy"`
	Verdict        string         `json:"verdict"`
	CopyLagS       float64        `json:"copy_lag_s"`
	ParseLagS      float64        `json:"parse_lag_s"`
	Held           int            `json:"held"`
	FreeBytes      int64          `json:"free_bytes"`
	StageCounts    map[string]int `json:"stage_counts"`
	ParserVersions map[string]int `json:"parser_versions"`
	SchemaVersion  int            `json:"schema_version"`
	ProtocolVer    int            `json:"protocol_version"`

	// Short is the one word for status --short: ok, behind, held or down.
	Short    string    `json:"short"`
	CaughtUp bool      `json:"caught_up"`
	Degraded bool      `json:"degraded,omitempty"`
	Backfill *Backfill `json:"backfill,omitempty"`
	Socket   string    `json:"socket,omitempty"`
	// The offline answer fills these from disk.
	LaunchAgent string `json:"launch_agent,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	StateBytes  int64  `json:"state_bytes,omitempty"`
}

// Backfill is the first-run progress: files copied of files seen, and an
// estimate of the seconds left (0 when unknown).
type Backfill struct {
	Copied  int     `json:"copied"`
	Total   int     `json:"total"`
	Parsed  int     `json:"parsed"`
	EtaS    float64 `json:"eta_s"`
	Running bool    `json:"running"`
}

// RootCoverage is one watched root.
type RootCoverage struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Files   int    `json:"files"`
}

// FileCoverage is one file's copy and parse state.
type FileCoverage struct {
	FileID        int64   `json:"file_id"`
	Source        string  `json:"source"`
	Path          string  `json:"path"`
	State         string  `json:"state"`
	Size          int64   `json:"size"`
	CopyOffset    int64   `json:"copy_offset"`
	ParseOffset   int64   `json:"parse_offset"`
	CopyLagBytes  int64   `json:"copy_lag_bytes"`
	ParseLagBytes int64   `json:"parse_lag_bytes"`
	CopyLagS      float64 `json:"copy_lag_s"`
}

// Worst names the file with the largest copy lag.
type Worst struct {
	FileID       int64  `json:"file_id"`
	Path         string `json:"path"`
	CopyLagBytes int64  `json:"copy_lag_bytes"`
}

// CoverageSummary is coverage --view summary.
type CoverageSummary struct {
	Files              int            `json:"files"`
	CopyLagBytesMax    int64          `json:"copy_lag_bytes_max"`
	ParseLagBytesMax   int64          `json:"parse_lag_bytes_max"`
	CopyLagSecondsMax  float64        `json:"copy_lag_seconds_max"`
	ParseLagSecondsMax float64        `json:"parse_lag_seconds_max"`
	BadLines           int            `json:"bad_lines"`
	UnknownKeys        []UnknownKey   `json:"unknown_keys"`
	UnparsedKinds      []UnparsedKind `json:"unparsed_kinds"`
	Held               int            `json:"held"`
	Settled            int            `json:"settled"`
	Removed            int            `json:"removed"`
	Worst              *Worst         `json:"worst"`
}

// Coverage is the coverage answer. Files is set only for --view files.
type Coverage struct {
	CoverageSummary
	CaughtUp bool           `json:"caught_up"`
	Degraded bool           `json:"degraded,omitempty"`
	Backfill *Backfill      `json:"backfill,omitempty"`
	Roots    []RootCoverage `json:"roots"`
	FileList []FileCoverage `json:"file_list,omitempty"`
}

// CoverageRequest is the POST /v1/coverage body.
type CoverageRequest struct {
	View string `json:"view,omitempty"` // summary (default) or files
}

// UnknownKey is a key a parser does not recognize, counted once per distinct
// dotted path per source and record type.
type UnknownKey struct {
	Source string `json:"source"`
	Type   string `json:"record_type,omitempty"`
	Path   string `json:"path"`
	Count  int    `json:"count"`
}

// UnparsedKind is a kind of file no parser reads yet, with its file count.
type UnparsedKind struct {
	Kind  string `json:"kind"`
	Files int    `json:"files"`
}
