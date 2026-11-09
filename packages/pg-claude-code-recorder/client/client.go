// Package client is the Go client of the recorder's wire protocol (HTTP/1.1 with
// JSON bodies over a Unix socket), for daemons that read the recorder. It lives
// outside internal/ so other workspace modules can import it.
//
// Every client starts with Dial, which performs hello and refuses a daemon that
// speaks a higher protocol_version than this package knows. Errors are typed:
// match them with errors.Is against ErrDown, ErrBehind, ErrBusy,
// ErrUnavailable, ErrBadRequest, ErrNotFound and ErrCursorExpired.
//
// Retry guidance: busy and unavailable back off from 1 s doubling to 30 s
// (Backoff); a daemon restart closes the connection and a Feed iterator
// reconnects with the same cursor; a client persists its own cursor (the
// iterator's Cursor), and hello reports caught_up and degraded. There is no
// fallback to Claude's files: when the daemon is offline the client says so.
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	iclient "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// ProtocolVersion is the highest wire protocol version this package speaks.
const ProtocolVersion = wire.ProtocolVersion

// EnvSocket names the environment variable that points at the daemon's socket.
const EnvSocket = config.EnvSocket

// The request and answer shapes of the wire contract.
type (
	// Hello is the answer to hello.
	Hello = wire.Hello
	// Filter is the JSON filter language.
	Filter = query.Filter
	// Range bounds a value in a filter.
	Range = query.Range
	// QueryRequest is the query operation's body.
	QueryRequest = query.Request
	// Agg and Order are query aggregates and orderings.
	Agg   = query.Agg
	Order = query.Order
	// QueryAnswer is the query answer.
	QueryAnswer = query.Answer
	// InventoryRequest, InventoryAnswer and InventoryFile are the files.inventory
	// operation's shapes.
	InventoryRequest = query.InventoryRequest
	InventoryAnswer  = query.InventoryAnswer
	InventoryFile    = query.InventoryFile
	// SearchRequest is the search operation's body.
	SearchRequest = query.SearchRequest
	// SearchAnswer is the search answer.
	SearchAnswer = query.SearchAnswer
	// RecordRef names a record by its position.
	RecordRef = query.RecordRef
	// Entry is one feed or snapshot entry (a reset or a record).
	Entry = feed.Entry
	// FeedAnswer is one page of the feed.
	FeedAnswer = feed.Answer
	// SnapshotRequest and SnapshotAnswer are the snapshot operation's shapes.
	SnapshotRequest = feed.SnapshotRequest
	SnapshotAnswer  = feed.SnapshotAnswer
	// RawRequest and RawAnswer are the raw operation's shapes.
	RawRequest = feed.RawRequest
	RawAnswer  = feed.RawAnswer
)

// The typed errors.
var (
	// ErrDown: nothing answers on the socket ("daemon offline").
	ErrDown = errors.New("recorder daemon offline")
	// ErrBehind: the recorder is catching up.
	ErrBehind = errors.New("recorder is behind")
	// ErrBusy: the daemon's admission limit was reached.
	ErrBusy = errors.New("recorder is busy")
	// ErrQueryTimeout: the read hit the daemon deadline; retry, ideally narrower.
	ErrQueryTimeout = errors.New("recorder query timed out")
	// ErrUnavailable: the daemon could not answer (including a closed connection).
	ErrUnavailable = errors.New("recorder unavailable")
	// ErrBadRequest: the daemon refused the request as malformed.
	ErrBadRequest = errors.New("recorder bad request")
	// ErrNotFound: the daemon has no such thing.
	ErrNotFound = errors.New("recorder not found")
	// ErrCursorExpired: the cursor is from an old epoch, below the retention
	// floor or beyond the head; resync from Snapshot.
	ErrCursorExpired = errors.New("recorder cursor expired")
	// ErrProtocol: the daemon speaks a higher protocol_version than this client.
	ErrProtocol = errors.New("recorder protocol version is newer than this client knows")
)

// Error is a daemon or transport error; errors.Is matches its sentinel.
type Error struct {
	// Name is the stable wire name (bad_request, cursor_expired, daemon_offline, ...).
	Name    string
	Message string
	// Cursor is the cursor to resync from, set with ErrCursorExpired.
	Cursor   string
	sentinel error
}

func (e *Error) Error() string { return e.Name + ": " + e.Message }

// Is lets errors.Is(err, ErrBusy) and friends match.
func (e *Error) Is(target error) bool { return e.sentinel != nil && target == e.sentinel }

// Unwrap exposes the sentinel.
func (e *Error) Unwrap() error { return e.sentinel }

func sentinelOf(name string) error {
	switch name {
	case wire.ErrDaemonOffline:
		return ErrDown
	case wire.ErrBehind:
		return ErrBehind
	case wire.ErrBusy:
		return ErrBusy
	case wire.ErrQueryTimeout:
		return ErrQueryTimeout
	case wire.ErrUnavailable:
		return ErrUnavailable
	case wire.ErrBadRequest:
		return ErrBadRequest
	case wire.ErrNotFound:
		return ErrNotFound
	case wire.ErrCursorExpired:
		return ErrCursorExpired
	}
	return nil
}

func convert(err error) error {
	if err == nil {
		return nil
	}
	var we *wire.Error
	if errors.As(err, &we) {
		return &Error{Name: we.Name, Message: we.Message, Cursor: we.Cursor, sentinel: sentinelOf(we.Name)}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{Name: wire.ErrUnavailable, Message: err.Error(), sentinel: ErrUnavailable}
}

// Options configures Dial.
type Options struct {
	// Socket is the daemon's socket. Empty discovers it: the environment
	// variable PG_CLAUDE_CODE_RECORDER_SOCKET, else the default path
	// $XDG_STATE_HOME/pg-claude-code-recorder/recorder.sock.
	Socket string
	// Name is sent with each request (the per-client metric label).
	Name string
	// ConnectTimeout bounds each connect (2 s when zero).
	ConnectTimeout time.Duration
	// RequestTimeout bounds a request other than a waiting feed (30 s when zero).
	RequestTimeout time.Duration
	// Getenv reads the environment (os.Getenv when nil).
	Getenv func(string) string
	// Sleep waits between retries (a test seam; time.Sleep honoring ctx when nil).
	Sleep func(ctx context.Context, d time.Duration) error
}

// DefaultSocket is the socket Dial discovers without Options.Socket.
func DefaultSocket(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := getenv(EnvSocket); v != "" {
		return v
	}
	return filepath.Join(config.StateHome(getenv), config.SocketName)
}

// Client talks to one recorder daemon. It is safe for concurrent use.
type Client struct {
	opt   Options
	c     *iclient.Client
	hello Hello
}

// Dial discovers the socket, connects (with the connect timeout), performs hello
// and refuses a higher protocol_version. A daemon that is not running is
// ErrDown.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	if opts.Socket == "" {
		opts.Socket = DefaultSocket(opts.Getenv)
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 2 * time.Second
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	sock, ct := opts.Socket, opts.ConnectTimeout
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: ct}
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}
	c := &Client{opt: opts, c: &iclient.Client{Socket: sock, Name: opts.Name, HTTP: &http.Client{Transport: tr}}}
	h, err := c.Hello(ctx)
	if err != nil {
		return nil, err
	}
	if h.ProtocolVersion > ProtocolVersion {
		return nil, &Error{
			Name:     "protocol",
			Message:  fmt.Sprintf("daemon protocol_version %d is newer than this client's %d; upgrade the client", h.ProtocolVersion, ProtocolVersion),
			sentinel: ErrProtocol,
		}
	}
	return c, nil
}
