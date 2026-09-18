// client.go: the socket dialer/round-tripper — this backend's own
// injectable transport seam, mirroring
// cmd/pg-connector-thread-slack/internal/runner.go's "injectable seam;
// production execs/dials for real, tests inject a fake" structural
// pattern, adapted to a net.Dial("unix", ...) socket client rather than an
// os/exec transport.
//
// packages/osx-bridge-api is a WHOLLY SEPARATE Go module (its own go.mod,
// module path github.com/phillipgreenii/osx-bridge-api) from this module,
// and every symbol this backend needs from it lives under an internal/
// directory — Go's own internal-visibility rule forbids importing
// anything under .../osx-bridge-api/internal/... from a package whose
// import path does not share that prefix, which this backend's own
// cmd/pg-connector-calendar-osx-bridge import path never can. So this file
// defines its OWN LOCAL Go types mirroring osx-bridge-api's JSON field
// shapes BY STRUCT TAG, replicated from reading
// packages/osx-bridge-api/internal/wire/{envelope.go,errors.go} and
// packages/osx-bridge-api/internal/calendarapi/types.go directly — never
// an import, and never a replace directive pulling that module in.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// socketEnvVar and defaultSocketPath's own construction mirror
// packages/osx-bridge-api/cmd/osx-bridge-api/main.go's identical
// socketEnvVar/defaultSocketPath resolution EXACTLY [landed: pg2-p9ap3] —
// this backend is a CLIENT of that same daemon socket, so it resolves the
// identical env var/default rather than inventing a second,
// backend-specific one.
const socketEnvVar = "OSX_BRIDGE_API_SOCKET"

// wireProtocolVersion mirrors osx-bridge-api's internal/wire.ProtocolVersion
// [landed: pg2-p9ap3] — the socket transport's own envelope version, sent
// on every request this client makes.
const wireProtocolVersion = 1

// serviceName, opCalendars, and opEvents mirror osx-bridge-api's
// internal/calendarapi.{ServiceName,OpCalendars,OpEvents} [landed:
// pg2-p9ap3] exactly, by value (never imported).
const (
	serviceName = "calendar"
	opCalendars = "calendars"
	opEvents    = "events"
)

// ResolveSocketPath resolves the daemon's socket path: socketEnvVar when
// getenv reports it set, else
// ${XDG_STATE_HOME:-$HOME/.local/state}/osx-bridge-api/osx-bridge-api.sock
// — byte-for-byte the same algorithm
// packages/osx-bridge-api/cmd/osx-bridge-api/main.go's own
// socketEnvVar/defaultSocketPath resolution uses [landed: pg2-p9ap3],
// since this backend is a client of that same socket and MUST resolve the
// identical default a caller who never set the env var would still reach.
func ResolveSocketPath(getenv func(string) string) (string, error) {
	if sock := getenv(socketEnvVar); sock != "" {
		return sock, nil
	}
	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("neither " + socketEnvVar + ", XDG_STATE_HOME, nor HOME is set")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "osx-bridge-api", "osx-bridge-api.sock"), nil
}

// ---------------------------------------------------------------------
// Local mirrors of osx-bridge-api's own wire shapes (field-shape
// reference only — see this file's package doc comment for why these are
// independently-defined types, never an import).
// ---------------------------------------------------------------------

// wireRequest mirrors internal/wire.Request [landed: pg2-p9ap3].
type wireRequest struct {
	ProtocolVersion int             `json:"protocolVersion,omitempty"`
	Service         string          `json:"service"`
	Op              string          `json:"op"`
	Args            json.RawMessage `json:"args,omitempty"`
}

// wireErrorBody mirrors internal/wire.ErrorBody [landed: pg2-p9ap3]. Code
// MUST be one of the closed six-value taxonomy classifyWireError below
// maps.
type wireErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wireResponse mirrors internal/wire.Response [landed: pg2-p9ap3].
type wireResponse struct {
	ProtocolVersion int             `json:"protocolVersion"`
	SchemaVersion   int             `json:"schemaVersion,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *wireErrorBody  `json:"error,omitempty"`
}

// ---------------------------------------------------------------------
// Local mirrors of osx-bridge-api's calendarapi JSON shapes (field-shape
// reference only, see this file's package doc comment).
// ---------------------------------------------------------------------

// apiCalendar mirrors internal/calendarapi.Calendar [landed: pg2-p9ap3].
type apiCalendar struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Type     string `json:"type"`
	Color    string `json:"color,omitempty"`
	Source   string `json:"source,omitempty"`
	ReadOnly bool   `json:"readOnly"`
}

// apiAttendee mirrors internal/calendarapi.Attendee [landed: pg2-p9ap3].
type apiAttendee struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status,omitempty"`
}

// apiEvent mirrors internal/calendarapi.Event [landed: pg2-p9ap3] — no
// Attachments field, no separate Organizer field (only Attendees), exactly
// as calendarapi/types.go itself declares.
type apiEvent struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	Start         time.Time     `json:"start"`
	End           time.Time     `json:"end"`
	AllDay        bool          `json:"allDay,omitempty"`
	CalendarID    string        `json:"calendarId"`
	Calendar      string        `json:"calendar,omitempty"`
	Location      string        `json:"location,omitempty"`
	Notes         string        `json:"notes,omitempty"`
	ConferenceURL string        `json:"conferenceUrl,omitempty"`
	Attendees     []apiAttendee `json:"attendees,omitempty"`
	SelfStatus    string        `json:"selfStatus,omitempty"`
	Recurring     bool          `json:"recurring,omitempty"`
	IsDetached    bool          `json:"isDetached,omitempty"`
}

// apiEventsQuery mirrors internal/calendarapi.EventsQuery [landed:
// pg2-p9ap3] — v1 filters by calendar ID, never by display name.
type apiEventsQuery struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	CalendarIDs []string  `json:"calendarIds,omitempty"`
	Search      string    `json:"search,omitempty"`
}

// wireCodeToSentinel maps osx-bridge-api's six wire error codes [landed:
// pg2-p9ap3] onto this backend's own outward-facing pkg/scriptout.Err*
// sentinels — a straight six-way string-to-sentinel mapping, not a new
// taxonomy and not a claim of exact six-way equality with scriptout.Err*'s
// own seven (scriptout carries a seventh sentinel, ErrQueryNotRecognized,
// produced only by pg-connector's own "list" dispatch layer resolving a
// named query — never something this backend itself needs to emit, since
// this backend answers "list"/"list_events" requests directly rather than
// resolving another backend's named queries).
var wireCodeToSentinel = map[string]error{
	"not_found":        scriptout.ErrNotFound,
	"unauthenticated":  scriptout.ErrUnauthenticated,
	"unavailable":      scriptout.ErrUnavailable,
	"unknown_op":       scriptout.ErrUnknownOp,
	"version_mismatch": scriptout.ErrVersionMismatch,
	"invalid_argument": scriptout.ErrInvalidArgument,
}

// classifyWireError maps a wire error body to a scriptout-sentinel-wrapped
// Go error, falling back to ErrUnavailable for a code outside the
// six-value taxonomy (should not happen against a well-behaved daemon).
func classifyWireError(body *wireErrorBody) error {
	sentinel, ok := wireCodeToSentinel[body.Code]
	if !ok {
		sentinel = scriptout.ErrUnavailable
	}
	return scriptout.WrapError(sentinel, "osx-bridge-api: "+body.Message)
}

// Transport is this backend's own injectable seam over the calendar
// service's two ops — SocketClient (below) is the production
// implementation; backend_test.go injects a stub, exercising the
// translation layer against no live socket at all [design: acceptance
// criterion 1].
type Transport interface {
	Calendars(ctx context.Context) ([]apiCalendar, error)
	Events(ctx context.Context, q apiEventsQuery) ([]apiEvent, error)
}

// SocketClient is the production Transport: it dials
// OSX_BRIDGE_API_SOCKET (or the resolved default path) fresh for every
// call — one connection per call, mirroring osx-bridge-api's own
// ServeConn's "one request, one response, one connection" framing
// [landed: pg2-p9ap3] — writes one locally-defined wireRequest, and reads
// back one locally-defined wireResponse.
type SocketClient struct {
	// SocketPath, when non-empty, pins the socket path directly, bypassing
	// env resolution entirely. Optional — tests set this to a disposable
	// fake listener's path; production (NewSocketClient) leaves it empty
	// and resolves it fresh via ResolveSocketPath on every call, mirroring
	// cmd/pg-connector-thread-slack/internal.CLIRunner's identical
	// BinaryOverride precedent.
	SocketPath string
	// Getenv resolves socketEnvVar/XDG_STATE_HOME/HOME when SocketPath is
	// unset. Optional — nil means os.Getenv.
	Getenv func(string) string
}

// NewSocketClient returns a SocketClient using the process env and the
// real daemon socket.
func NewSocketClient() *SocketClient { return &SocketClient{} }

var _ Transport = (*SocketClient)(nil)

func (c *SocketClient) resolvePath() (string, error) {
	if c.SocketPath != "" {
		return c.SocketPath, nil
	}
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return ResolveSocketPath(getenv)
}

// call dials the resolved socket path, writes one wireRequest for
// (op, args), and returns the decoded response's own Result (or a
// classified error for an error-branch response, a dial failure, a write
// failure, or a malformed reply). ctx's own deadline (already bounded by
// pkg/scriptout's DefaultExecTimeout, threaded in by serveLoop) is applied
// to both the dial and the connection's read/write deadline, so a wedged
// daemon cannot hang this call indefinitely.
func (c *SocketClient) call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	path, err := c.resolvePath()
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "osx-bridge-api: resolve socket path: "+err.Error())
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("osx-bridge-api: dial %s: %v", path, err))
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "osx-bridge-api: marshal args: "+err.Error())
	}
	req := wireRequest{ProtocolVersion: wireProtocolVersion, Service: serviceName, Op: op, Args: argsRaw}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "osx-bridge-api: write request: "+err.Error())
	}

	var resp wireResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "osx-bridge-api: read response: "+err.Error())
	}
	if resp.Error != nil {
		return nil, classifyWireError(resp.Error)
	}
	return resp.Result, nil
}

// Calendars implements Transport via the "calendars" op (no args).
func (c *SocketClient) Calendars(ctx context.Context) ([]apiCalendar, error) {
	raw, err := c.call(ctx, opCalendars, struct{}{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Calendars []apiCalendar `json:"calendars"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "osx-bridge-api: decode calendars result: "+err.Error())
	}
	return result.Calendars, nil
}

// Events implements Transport via the "events" op.
func (c *SocketClient) Events(ctx context.Context, q apiEventsQuery) ([]apiEvent, error) {
	raw, err := c.call(ctx, opEvents, q)
	if err != nil {
		return nil, err
	}
	var result struct {
		Events []apiEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "osx-bridge-api: decode events result: "+err.Error())
	}
	return result.Events, nil
}
