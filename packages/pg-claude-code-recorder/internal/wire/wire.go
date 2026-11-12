// Package wire holds what the daemon and the command share: the protocol
// version, the error names with their exit codes, and the stable answer shapes.
// The framing is HTTP/1.1 with JSON bodies over a Unix socket: GET /v1/hello
// and POST /v1/<operation>.
package wire

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ProtocolVersion is the wire protocol version reported by status and hello.
const ProtocolVersion = 1

// ClientHeader carries the caller's --client name.
const ClientHeader = "X-Recorder-Client"

// DeadlineHeader carries the client's own budget for the request in
// milliseconds (additive: an old client sends none). The server clamps it to at
// least MinDeadline and never above query.timeout, and applies it as the
// request deadline when it is shorter than query.timeout.
const DeadlineHeader = "X-Recorder-Deadline-Ms"

// MinDeadline is the floor of an accepted DeadlineHeader.
const MinDeadline = time.Second

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
	// ExitShutdownOverrun is serve's hard exit when shutdown outlasts its deadline.
	ExitShutdownOverrun = 10
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
	// ErrQueryTimeout: a read hit the daemon deadline (query.timeout); retry, ideally narrower.
	ErrQueryTimeout = "query_timeout"
	// ErrCancelled: the request was cancelled (the client went away). It is not a
	// timeout and not an internal failure.
	ErrCancelled = "cancelled"
	// Exit 8 preconditions.
	ErrRemovalDisabled = "removal_disabled"
	ErrTargetExists    = "target_exists"
	ErrHeldByLive      = "held_by_live_process"
	// ErrSchemaRebuildRequired: serve refused to start because index.db was
	// written by an older schema; an operator runs the rebuild command.
	ErrSchemaRebuildRequired = "schema_rebuild_required"
)

// ExitFor maps a stable error name to the recorder command's exit code.
func ExitFor(name string) int {
	switch name {
	case ErrBadRequest, ErrUsage:
		return ExitUsage
	case ErrWaitTimeout, ErrQueryTimeout:
		return ExitTimeout
	case ErrUnavailable, ErrDaemonOffline:
		return ExitOffline
	case ErrNotFound:
		return ExitNotFound
	case ErrBusy, ErrBehind:
		return ExitDegraded
	case ErrCursorExpired:
		return ExitCursor
	case ErrRemovalDisabled, ErrTargetExists, ErrHeldByLive, ErrSchemaRebuildRequired:
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
	case ErrQueryTimeout:
		return http.StatusGatewayTimeout
	case ErrBusy, ErrBehind:
		return http.StatusTooManyRequests
	case ErrCancelled:
		// 499: the nginx convention for a client that closed the request.
		return 499
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
	// Cursor is the cursor to resync from, set on cursor_expired.
	Cursor string `json:"cursor,omitempty"`
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
	// StatusAgeS is the age of the cached copy and parse figures (the daemon
	// recomputes them in the background, so status never scans the files).
	StatusAgeS float64 `json:"status_age_s,omitempty"`
	// CrashedRecent and SuspectedRecent count the Conversations that ended crashed,
	// and crashed plus unknown with a stale registry file, in the last 7 days. The
	// daemon counts them from its Conversation model with EndLabel in the status
	// refresh loop, so they match conversations --crashed and --suspected-crash;
	// they are absent before the first refresh or when it failed.
	CrashedRecent   *int `json:"crashed_recent,omitempty"`
	SuspectedRecent *int `json:"suspected_recent,omitempty"`
	// The offline answer fills these from disk.
	LaunchAgent string `json:"launch_agent,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	StateBytes  int64  `json:"state_bytes,omitempty"`

	// LastTick describes the last completed settle tick; it is absent (never
	// zero) before the first one completes.
	LastTick *LastTick `json:"last_tick,omitempty"`
	// Loops maps each loop name to its last pass. A loop that has not completed a
	// pass has no last_duration_ms, last_outcome or age_s.
	Loops map[string]LoopStatus `json:"loops,omitempty"`

	// StatExceptions is how many files the last status refresh had to Lstat (a
	// file that lags or was touched recently); every other file's size is the one
	// the copier last recorded, so a change the watcher missed shows only after
	// the next sweep or a coverage run. LastFullStatAgeS is the age of the last
	// coverage run or watcher sweep.
	StatExceptions   *int     `json:"stat_exceptions,omitempty"`
	LastFullStatAgeS *float64 `json:"last_full_stat_age_s,omitempty"`
}

// LastTick is the last completed settle tick: how long it took, how many
// Conversations it examined and the seconds since it ended.
type LastTick struct {
	DurationMS int64 `json:"duration_ms"`
	Examined   int   `json:"examined"`
	AgeS       int64 `json:"age_s"`
}

// LoopStatus is one loop's last pass and its now-running pass, all whole numbers
// (milliseconds or seconds). Stalled is set when the loop is past its stall
// bound (internal/obs StallAgeBound and InFlightStallBound).
type LoopStatus struct {
	LastDurationMS *int64 `json:"last_duration_ms,omitempty"`
	LastOutcome    string `json:"last_outcome,omitempty"`
	AgeS           *int64 `json:"age_s,omitempty"`
	InFlightS      int64  `json:"in_flight_s"`
	Stalled        bool   `json:"stalled,omitempty"`
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
	// SizeSource says where Size came from in the status snapshot: "recorded"
	// (the copier's last seen size) or "stat" (a Lstat). The coverage command
	// always stats and leaves it empty.
	SizeSource string `json:"size_source,omitempty"`
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
	// RemovedConversations counts the Conversations whose files are gone from the
	// Claude directory (their bytes are only in the archives).
	RemovedConversations int    `json:"removed_conversations"`
	Worst                *Worst `json:"worst"`
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

// Hello is the GET /v1/hello answer: every client starts with it.
type Hello struct {
	Service         string   `json:"service"`
	Version         string   `json:"version"`
	ProtocolVersion int      `json:"protocol_version"`
	SchemaVersion   int      `json:"schema_version"`
	Epoch           int      `json:"epoch"`
	Capabilities    []string `json:"capabilities"`
	CaughtUp        bool     `json:"caught_up"`
	Degraded        bool     `json:"degraded"`
}

// FromErr maps an error from a read to its wire error: a context deadline is
// the retryable query_timeout, a cancelled context is cancelled, anything else
// is internal.
func FromErr(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return Errf(ErrQueryTimeout, "the read hit the daemon's query deadline: "+err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return Errf(ErrCancelled, "the request was cancelled: "+err.Error())
	}
	return Errf(ErrInternal, err.Error())
}
