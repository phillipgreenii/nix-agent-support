// client.go: the socket dialer/round-tripper — this backend's own
// injectable transport seam, structurally mirroring
// cmd/pg-connector-calendar-osx-bridge/internal/client.go. ADR 0062
// principle 6 forbids a shared library across backends, so this file is
// its own COPY of the socket-client code, never an import of another
// backend's package.
//
// phillipgreenii-nix-support-apps/packages/pg-osx-bridge-api is a WHOLLY
// SEPARATE Go module (its own go.mod) from this module, and every symbol
// this backend needs from it lives under an internal/ directory — Go's own
// internal-visibility rule forbids importing it from here. So this file
// defines its OWN LOCAL Go types mirroring pg-osx-bridge-api's JSON field
// shapes BY STRUCT TAG, replicated from reading
// phillipgreenii-nix-support-apps/packages/pg-osx-bridge-api/internal/wire/{envelope.go,errors.go}
// and .../internal/mailapi/types.go (phillipgreenii-nix-support-apps ADR
// 0047, "mail" service allowlist and wire contract) directly — never an
// import, and never a replace directive pulling that module in.
//
// The bridge rejects unknown request fields with invalid_argument, so the
// apiXxx request types below carry EXACTLY the bridge's own argument keys.
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

// socketEnvVar mirrors pg-osx-bridge-api's own socketEnvVar/
// defaultSocketPath resolution EXACTLY: this backend is a CLIENT of that
// same daemon socket, so it resolves the identical env var/default rather
// than inventing a second, backend-specific one.
const socketEnvVar = "PG_OSX_BRIDGE_API_SOCKET"

// wireProtocolVersion mirrors pg-osx-bridge-api's internal/wire.ProtocolVersion
// — the socket transport's own envelope version, sent on every request.
const wireProtocolVersion = 1

// serviceName and the op* constants mirror pg-osx-bridge-api's
// internal/mailapi.{ServiceName,Op*} exactly, by value (never imported).
// There is deliberately NO delete-shaped op constant here, and none MUST
// ever be added (invariant INV-MAIL-1).
const (
	serviceName       = "mail"
	opList            = "list"
	opShow            = "show"
	opSearch          = "search"
	opMarkRead        = "mark-read"
	opMarkUnread      = "mark-unread"
	opArchive         = "archive"
	opUnarchive       = "unarchive"
	opFetchAttachment = "fetch-attachment"
)

// ResolveSocketPath resolves the daemon's socket path: socketEnvVar when
// getenv reports it set, else
// ${XDG_STATE_HOME:-$HOME/.local/state}/pg-osx-bridge-api/pg-osx-bridge-api.sock
// — byte-for-byte the same algorithm pg-osx-bridge-api's own
// cmd/pg-osx-bridge-api/main.go uses, since this backend is a client of
// that same socket.
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
	return filepath.Join(stateHome, "pg-osx-bridge-api", "pg-osx-bridge-api.sock"), nil
}

// ---------------------------------------------------------------------
// Local mirrors of pg-osx-bridge-api's own wire envelope.
// ---------------------------------------------------------------------

// wireRequest mirrors internal/wire.Request.
type wireRequest struct {
	ProtocolVersion int             `json:"protocolVersion,omitempty"`
	Service         string          `json:"service"`
	Op              string          `json:"op"`
	Args            json.RawMessage `json:"args,omitempty"`
}

// wireErrorBody mirrors internal/wire.ErrorBody. Code MUST be one of the
// closed six-value taxonomy classifyWireError below maps.
type wireErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wireResponse mirrors internal/wire.Response.
type wireResponse struct {
	ProtocolVersion int             `json:"protocolVersion"`
	SchemaVersion   int             `json:"schemaVersion,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *wireErrorBody  `json:"error,omitempty"`
}

// ---------------------------------------------------------------------
// Local mirrors of pg-osx-bridge-api's mailapi JSON shapes.
// ---------------------------------------------------------------------

// apiAttachment mirrors internal/mailapi.Attachment.
type apiAttachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

// apiMessage mirrors internal/mailapi.Message.
type apiMessage struct {
	ID           string          `json:"id"`
	Subject      string          `json:"subject"`
	Sender       string          `json:"sender"`
	DateReceived time.Time       `json:"dateReceived"`
	Read         bool            `json:"read"`
	Flagged      bool            `json:"flagged"`
	Mailbox      string          `json:"mailbox"`
	Attachments  []apiAttachment `json:"attachments"`
}

// apiMessageDetail mirrors internal/mailapi.MessageDetail (the embedded
// Message's fields are flattened on the wire).
type apiMessageDetail struct {
	apiMessage
	To   []string `json:"to"`
	Cc   []string `json:"cc"`
	Body string   `json:"body"`
}

// apiListQuery mirrors internal/mailapi.ListQuery. Since is sent even when
// zero (the bridge reads the zero time as "no lower bound").
type apiListQuery struct {
	Mailbox    string    `json:"mailbox,omitempty"`
	UnreadOnly bool      `json:"unreadOnly,omitempty"`
	Since      time.Time `json:"since"`
	Limit      int       `json:"limit,omitempty"`
}

// apiSearchQuery mirrors internal/mailapi.SearchQuery.
type apiSearchQuery struct {
	Query   string    `json:"query"`
	Mailbox string    `json:"mailbox,omitempty"`
	Since   time.Time `json:"since"`
	Limit   int       `json:"limit,omitempty"`
}

// apiMessageRef mirrors internal/mailapi.MessageRef.
type apiMessageRef struct {
	ID string `json:"id"`
}

// apiAttachmentRef mirrors internal/mailapi.AttachmentRef. There is no
// destination field: where the file is saved is bridge-side configuration,
// never a request field (operator ruling, 2026-10-05).
type apiAttachmentRef struct {
	MessageID    string `json:"messageId"`
	AttachmentID string `json:"attachmentId"`
}

// apiAttachmentFile mirrors internal/mailapi.AttachmentFile.
type apiAttachmentFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// wireCodeToSentinel maps pg-osx-bridge-api's six wire error codes onto this
// backend's own outward-facing pkg/scriptout.Err* sentinels — a straight
// six-way string-to-sentinel mapping (ADR 0062's closed taxonomy).
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
	return scriptout.WrapError(sentinel, "pg-osx-bridge-api: "+body.Message)
}

// Transport is this backend's own injectable seam over the mail service's
// ops — SocketClient (below) is the production implementation;
// backend_test.go injects a stub, exercising the translation layer against
// no live socket and no Mail.app at all. It has NO delete method (INV-MAIL-1).
type Transport interface {
	List(ctx context.Context, q apiListQuery) ([]apiMessage, error)
	Show(ctx context.Context, id string) (apiMessageDetail, error)
	Search(ctx context.Context, q apiSearchQuery) ([]apiMessage, error)
	SetRead(ctx context.Context, id string, read bool) error
	Archive(ctx context.Context, id string) error
	Unarchive(ctx context.Context, id string) error
	FetchAttachment(ctx context.Context, messageID, attachmentID string) (apiAttachmentFile, error)
}

// SocketClient is the production Transport: it dials
// PG_OSX_BRIDGE_API_SOCKET (or the resolved default path) fresh for every
// call — one connection per call, mirroring pg-osx-bridge-api's own
// "one request, one response, one connection" framing — writes one
// locally-defined wireRequest, and reads back one locally-defined
// wireResponse.
type SocketClient struct {
	// SocketPath, when non-empty, pins the socket path directly, bypassing
	// env resolution entirely. Optional — tests set this to a disposable
	// fake listener's path; production (NewSocketClient) leaves it empty
	// and resolves it fresh via ResolveSocketPath on every call.
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
// failure, or a malformed reply). ctx's own deadline is applied to both
// the dial and the connection's read/write deadline, so a wedged daemon
// cannot hang this call indefinitely.
func (c *SocketClient) call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	path, err := c.resolvePath()
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pg-osx-bridge-api: resolve socket path: "+err.Error())
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("pg-osx-bridge-api: dial %s: %v", path, err))
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "pg-osx-bridge-api: marshal args: "+err.Error())
	}
	req := wireRequest{ProtocolVersion: wireProtocolVersion, Service: serviceName, Op: op, Args: argsRaw}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pg-osx-bridge-api: write request: "+err.Error())
	}

	var resp wireResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pg-osx-bridge-api: read response: "+err.Error())
	}
	if resp.Error != nil {
		return nil, classifyWireError(resp.Error)
	}
	return resp.Result, nil
}

// decodeResult unmarshals raw into v, answering unavailable (a malformed
// reply) when it does not decode.
func decodeResult(op string, raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return scriptout.WrapError(scriptout.ErrUnavailable, "pg-osx-bridge-api: decode "+op+" result: "+err.Error())
	}
	return nil
}

// List implements Transport via the "list" op.
func (c *SocketClient) List(ctx context.Context, q apiListQuery) ([]apiMessage, error) {
	raw, err := c.call(ctx, opList, q)
	if err != nil {
		return nil, err
	}
	var result struct {
		Messages []apiMessage `json:"messages"`
	}
	if err := decodeResult(opList, raw, &result); err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// Show implements Transport via the "show" op.
func (c *SocketClient) Show(ctx context.Context, id string) (apiMessageDetail, error) {
	raw, err := c.call(ctx, opShow, apiMessageRef{ID: id})
	if err != nil {
		return apiMessageDetail{}, err
	}
	var result struct {
		Message apiMessageDetail `json:"message"`
	}
	if err := decodeResult(opShow, raw, &result); err != nil {
		return apiMessageDetail{}, err
	}
	return result.Message, nil
}

// Search implements Transport via the "search" op.
func (c *SocketClient) Search(ctx context.Context, q apiSearchQuery) ([]apiMessage, error) {
	raw, err := c.call(ctx, opSearch, q)
	if err != nil {
		return nil, err
	}
	var result struct {
		Messages []apiMessage `json:"messages"`
	}
	if err := decodeResult(opSearch, raw, &result); err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// SetRead implements Transport via the "mark-read"/"mark-unread" ops.
func (c *SocketClient) SetRead(ctx context.Context, id string, read bool) error {
	op := opMarkUnread
	if read {
		op = opMarkRead
	}
	_, err := c.call(ctx, op, apiMessageRef{ID: id})
	return err
}

// Archive implements Transport via the "archive" op.
func (c *SocketClient) Archive(ctx context.Context, id string) error {
	_, err := c.call(ctx, opArchive, apiMessageRef{ID: id})
	return err
}

// Unarchive implements Transport via the "unarchive" op.
func (c *SocketClient) Unarchive(ctx context.Context, id string) error {
	_, err := c.call(ctx, opUnarchive, apiMessageRef{ID: id})
	return err
}

// FetchAttachment implements Transport via the "fetch-attachment" op.
func (c *SocketClient) FetchAttachment(ctx context.Context, messageID, attachmentID string) (apiAttachmentFile, error) {
	raw, err := c.call(ctx, opFetchAttachment, apiAttachmentRef{MessageID: messageID, AttachmentID: attachmentID})
	if err != nil {
		return apiAttachmentFile{}, err
	}
	var f apiAttachmentFile
	if err := decodeResult(opFetchAttachment, raw, &f); err != nil {
		return apiAttachmentFile{}, err
	}
	return f, nil
}
